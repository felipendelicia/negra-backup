# Recuperación ante desastre

Qué hacer si se pierde el servidor de nat-backup, y qué hacer **hoy** para que ese
día se pueda hacer algo.

---

## 1. El problema

El servidor guarda en su base PostgreSQL, cifrados con `ENCRYPTION_KEY`:

| Dato | Columna | Para qué sirve |
|---|---|---|
| La passphrase con la que se cifró **cada backup** | `backup_jobs.encrypt_passphrase` | Sin ella, el `.enc` no se abre |
| Las credenciales de S3/SFTP | `storage_destinations.config` | Sin ellas, no se llega al archivo |

**No hay otra copia de esos valores en ningún lado.** El agente los recibe por
WebSocket en el momento de correr el job y no los persiste; los archivos `.enc`
en S3 no contienen ninguna pista sobre su passphrase (por diseño: si la
contuvieran, no estarían cifrados).

Entonces:

> Si se pierde el disco del servidor — y con él la base y el `.env` —, **todos los
> archivos `.enc` que están en S3 quedan indescifrables para siempre**. Años de
> backups convertidos en ruido. No hay soporte, ni reset, ni fuerza bruta que
> sirva: es AES-256-GCM con PBKDF2-SHA256 a 600.000 iteraciones.

Es el único punto de falla del sistema que **destruye datos retroactivamente**.
Un incendio en la oficina no toca los backups que están en S3; perder la base de
nat-backup sí los toca, todos, hasta el primero.

El kit de recuperación existe para cerrar ese agujero. Es un archivo único,
cifrado con una passphrase que elegís vos (**no** con `ENCRYPTION_KEY`, que es
justamente lo que el kit tiene que sobrevivir), que contiene las passphrases, las
credenciales, el mapa de "qué archivo corresponde a qué job", y un instructivo en
texto plano adentro del propio kit.

**El kit no es un backup.** No contiene ni un byte de tus datos: es el llavero.
Sirve si los archivos siguen en S3, y no sirve para nada si los archivos no están.

---

## 2. Generar el kit

```bash
export DATABASE_URL="postgres://nat_backup:...@localhost:5432/nat_backup?sslmode=disable"
export ENCRYPTION_KEY="$(grep '^ENCRYPTION_KEY=' /etc/nat-backup/.env | cut -d= -f2-)"

recovery-kit export --out ~/nat-backup-kit-$(date +%F).enc
```

Te pide la passphrase del kit dos veces (entrada oculta). Para automatizarlo,
pasala por variable de entorno — nunca por `--passphrase`, que queda visible en
`ps` para cualquier usuario de la máquina y en el historial del shell:

```bash
NAT_BACKUP_KIT_PASSPHRASE='...' recovery-kit export --out /ruta/kit.enc
```

Compilar la herramienta:

```bash
go build -o bin/recovery-kit ./cmd/recovery-kit
```

Al terminar, el comando reimprime el `sha256` del archivo, cuántos jobs y
destinos exportó, y **cuántas passphrases pudo recuperar**. Si aparece un bloque
`!! THIS KIT IS INCOMPLETE`, leelo entero: lo que falta ahí no se puede recuperar
después.

El kit se escribe con permisos `0600`, primero en un temporal, y recién se mueve
a su lugar definitivo después de volver a leerlo del disco y descifrarlo. Un kit
que no abre nunca llega a existir.

### Elegir la passphrase del kit

Mínimo 12 caracteres (la herramienta no acepta menos). Generala y guardala en un
gestor de contraseñas:

```bash
openssl rand -base64 24
```

**Si perdés la passphrase del kit, el kit es tan irrecuperable como lo que
protege.** Guardala en un lugar **distinto** del kit, y asegurate de que otra
persona pueda encontrar las dos cosas si vos no estás.

### Con qué frecuencia

| Cuándo | Por qué |
|---|---|
| **Cada vez que agregás o editás un job** | El kit es una foto. Un job creado después del último export no está en el kit, y sus backups no se pueden abrir. Este es el caso que muerde. |
| **Cada vez que agregás o cambiás un destino de storage** | Credenciales nuevas o rotadas. |
| **Después de rotar `ENCRYPTION_KEY`** (`docs/key-rotation.md`) | El kit viejo sigue sirviendo (guarda texto plano, no depende de la clave), pero conviene tener uno nuevo consistente con la base. |
| **Al menos una vez por mes**, aunque no haya cambios | Barato, y te obliga a verificar que el procedimiento todavía funciona. |

La herramienta avisa sola: `verify` marca `WARN` en un kit de más de 90 días.

### Verificarlo periódicamente

```bash
recovery-kit verify --file ~/nat-backup-kit-2026-08-04.enc
```

No imprime ni una passphrase ni una credencial: solo cuenta y chequea. Se puede
correr delante de otra persona, o desde cron. Devuelve exit code 1 si algo falla.

Un kit que nadie abrió desde el día que se generó es un kit que nadie sabe si
todavía sirve. Ponelo en el calendario, junto con la generación.

---

## 3. Dónde guardarlo (y dónde no)

Descifrado, el kit contiene **todos los secretos en claro**: cada passphrase de
backup y cada credencial de storage. Quien lo tenga junto con su passphrase puede
leer —y borrar— todos tus backups.

**NO:**

- En el servidor de nat-backup. Muere con lo que protege; es el escenario
  exacto para el que existe.
- En el mismo bucket S3 que los backups, y menos con las mismas credenciales:
  quien te compromete la cuenta se lleva el llavero también.
- En el repositorio git. Ni con `.gitignore`, ni "por un rato".
- Por mail, Slack o WhatsApp.
- En el mismo lugar que su propia passphrase.

**SÍ** (al menos dos copias, en dos lugares distintos):

- Como archivo adjunto en el gestor de contraseñas de la empresa (Bitwarden,
  1Password, KeePass). La passphrase va en una entrada **separada**.
- En un pendrive cifrado (LUKS / VeraCrypt) guardado físicamente en otro lado —
  la casa del responsable, una caja fuerte, la oficina del contador.
- En un bucket/proveedor **distinto** del que usan los backups, con credenciales
  distintas.
- Impreso y en un sobre lacrado en la caja fuerte, si el volumen lo permite
  (`recovery-kit inspect --show-secrets` da el JSON legible).

Regla práctica: si un solo incidente —un robo, un ransomware, una cuenta
comprometida— puede llevarse el kit **y** los backups **y** el servidor al mismo
tiempo, el kit está mal guardado.

---

## 4. Procedimiento de recuperación

Escenario: **el servidor no existe más.** Tenés el kit, su passphrase, y los
archivos siguen en S3.

### Paso 0 — Lo que necesitás

1. El kit y su passphrase.
2. Un binario `nat-backup-agent` (cualquier build reciente).
3. Nada más. No hace falta el servidor, ni la base, ni `ENCRYPTION_KEY`, ni este
   repositorio.

Si no tenés el binario a mano:

```bash
git clone https://github.com/felipendelicia/nat-backup && cd nat-backup
make build-agent          # deja bin/nat-backup-agent
go build -o bin/recovery-kit ./cmd/recovery-kit   # opcional, ver paso 1
```

### Paso 1 — Abrir el kit

Con la herramienta:

```bash
recovery-kit inspect --file kit.enc --show-secrets > /tmp/kit.json
chmod 600 /tmp/kit.json
```

Sin ella — el kit es un archivo nat-backup común (tar + zstd + AES-256-GCM), así
que lo abre el propio agente:

```bash
NAT_BACKUP_PASSPHRASE='<passphrase del kit>' \
  nat-backup-agent restore --file kit.enc --dest /tmp/kit-out

less /tmp/kit-out/recovery-kit/RECOVERY-INSTRUCTIONS.txt   # el instructivo
less /tmp/kit-out/recovery-kit/recovery-kit.json           # los datos
```

### Paso 2 — Encontrar el archivo

En el JSON, `runs` lista las corridas exitosas más recientes de cada job:

```json
{
  "job_name": "etc-nightly",
  "storage_path": "etc-nightly-9f3c….tar.zstd.enc",
  "checksum": "9f2b7c1e…",
  "storage_destination_id": "bbbb…",
  "encrypted": true
}
```

`storage_path` es exactamente el nombre del archivo dentro del destino. Si la
corrida que buscás es más vieja que lo que trae el kit, listá el destino a mano
(`aws s3 ls`, `sftp`, `ls`): los nombres siguen el patrón
`<nombre del job>-<id de la run>.tar.<zstd|gz>[.enc]`.

### Paso 3 — Armar el spec del destino

De `storage_destinations`, tomá la entrada con ese `storage_destination_id` y
copiá su `type` y su `config` **tal cual**:

```bash
cat > /tmp/dest.json <<'EOF'
{"type":"s3","config":{"bucket":"nat-backups","region":"us-east-1",
 "access_key":"AKIA…","secret_key":"…","prefix":"prod/"}}
EOF
chmod 600 /tmp/dest.json
```

### Paso 4 — Sacar la passphrase del job

De `jobs`, la entrada cuyo `id` coincide con el `job_id` de la run:

```json
{ "name": "etc-nightly", "encrypt_passphrase": "…", "passphrase_status": "recovered" }
```

`passphrase_status` tiene que decir `recovered`. Si dice `MISSING` o
`UNDECRYPTABLE`, esos archivos no se pueden abrir: el kit se generó cuando el dato
ya estaba roto.

### Paso 5 — Bajar y restaurar

Primero en seco, que no escribe nada y confirma que la passphrase es la correcta:

```bash
export NAT_BACKUP_PASSPHRASE='<encrypt_passphrase del paso 4>'

nat-backup-agent restore \
  --from-storage /tmp/dest.json \
  --name 'etc-nightly-9f3c….tar.zstd.enc' \
  --dest /srv/restore \
  --dry-run
```

Si lista los archivos esperados, repetí sin `--dry-run`:

```bash
nat-backup-agent restore \
  --from-storage /tmp/dest.json \
  --name 'etc-nightly-9f3c….tar.zstd.enc' \
  --dest /srv/restore
```

Si ya bajaste el archivo por otro medio (`aws s3 cp`, `sftp get`, una copia en
disco), saltate el spec:

```bash
nat-backup-agent restore --file ./etc-nightly-9f3c….tar.zstd.enc --dest /srv/restore
```

### Paso 6 — Verificar

```bash
sha256sum ./etc-nightly-9f3c….tar.zstd.enc    # tiene que dar igual a "checksum"
```

### Paso 7 — Limpiar

```bash
shred -u /tmp/dest.json /tmp/kit.json
rm -rf /tmp/kit-out
```

Son secretos en texto plano. El kit sellado se guarda; las copias descifradas se
borran.

### Si las credenciales de storage ya no sirven

Las credenciales del kit son las que valían el día del export. Si las rotaste o
te las revocaron, conseguí el archivo del bucket por cualquier otro medio y usá
`--file`. Solo el paso 5 depende de las credenciales; **la passphrase del paso 4
es lo que hace legible el archivo, y esa no la puede reemitir nadie.**

### Jobs de base de datos

El archivo contiene el dump, no una base viva. Adentro del `.tar` hay un único
archivo llamado `nat-backup-dump-<aleatorio>` (sin extensión: es el temporal que
usó el agente). Restaurás a disco como arriba y después lo cargás con el cliente
que corresponda:

```bash
ls /srv/restore/                                        # nat-backup-dump-1234567890
psql "$NUEVA_URL" < /srv/restore/nat-backup-dump-*      # postgres (pg_dump plano)
mysql -u … -p base < /srv/restore/nat-backup-dump-*     # mysql (mysqldump)
mongorestore --archive=/srv/restore/nat-backup-dump-*   # mongodb (mongodump --archive)
```

En SQLite no hay dump: el agente copia el archivo `.db` tal cual, así que lo que
sale de la restauración **ya es la base**. Renombralo y usalo.

El campo `source` del job registra la connection string desde la que se tomó el
dump.

---

## 5. Respaldar la base de Postgres (la vía rápida)

El kit rescata los backups. Un `pg_dump` rescata **la instalación entera**: jobs,
agentes, historial de runs, logs, destinos y usuarios. Cuando existe, es el camino
corto — y no reemplaza al kit, porque el dump está cifrado con `ENCRYPTION_KEY` y
sin esa clave no vale nada.

```bash
# Diario, desde cron, en la máquina del servidor
pg_dump "$DATABASE_URL" | gzip > /var/backups/nat-backup-db-$(date +%F).sql.gz
```

Restaurar en una máquina nueva:

```bash
createdb nat_backup
gunzip -c nat-backup-db-2026-08-04.sql.gz | psql "$DATABASE_URL"
```

Tres condiciones para que el dump sirva:

1. **Guardarlo fuera del servidor.** Un dump en el mismo disco que la base es
   decorativo.
2. **Guardar `ENCRYPTION_KEY` con él** (en el gestor de contraseñas, no al lado
   del dump). Sin la clave, el dump restaurado tiene las passphrases cifradas y
   es tan inútil como la base perdida.
3. **Probar la restauración alguna vez.** Un dump que nunca se restauró es una
   hipótesis.

Comparación honesta:

| | `pg_dump` | Kit de recuperación |
|---|---|---|
| Recupera la instalación completa | Sí | No (solo secretos y metadatos) |
| Sirve sin `ENCRYPTION_KEY` | **No** | **Sí** |
| Legible sin este repo | No | Sí (JSON + instructivo adentro) |
| Tamaño | Crece con el historial | Unos pocos KB |

**Hacé las dos cosas.** El `pg_dump` para volver a levantar el servicio; el kit
para el día en que el dump también se perdió, o en que aparece un `.enc` de 2024
y nadie sabe con qué se abre.

---

## 6. Qué NO cubre el kit

- **Los datos.** No contiene ningún archivo de backup. Si los `.enc` se
  perdieron, el kit no los trae de vuelta.
- **La base del servidor.** No reconstruye la instalación: no hay usuarios, ni
  logs, ni historial completo de runs. Para eso, `pg_dump` (sección 5).
- **Las API keys de los agentes.** Están guardadas como hash bcrypt y no se
  pueden revertir. Al reinstalar hay que generar keys nuevas y reconfigurar cada
  agente.
- **`ENCRYPTION_KEY`, `JWT_SECRET`, `ADMIN_PASSWORD`.** Van en el gestor de
  contraseñas, no en el kit. (El kit está hecho para servir *sin* ellos.)
- **La configuración SMTP / de notificaciones.** No hace falta para recuperar
  datos.
- **Runs viejas.** Por defecto guarda las 25 corridas exitosas más recientes de
  cada job (`--runs-per-job N` lo cambia). Las anteriores se encuentran listando
  el destino a mano; la passphrase del job es la misma para todas.
- **Cambios posteriores al export.** El kit es una foto. Un job creado ayer no
  está en el kit de la semana pasada.
- **Que el destino siga existiendo.** Si el bucket se borró, o las credenciales
  se revocaron sin reemplazo, el kit te dice qué había pero no lo trae.

---

## 7. Referencia rápida de comandos

```bash
# Generar
DATABASE_URL=… ENCRYPTION_KEY=… recovery-kit export --out kit-$(date +%F).enc

# Verificar (no imprime secretos; apto para cron)
recovery-kit verify --file kit.enc

# Ver el contenido, con credenciales enmascaradas
recovery-kit inspect --file kit.enc

# Ver el contenido en claro (para recuperar de verdad)
recovery-kit inspect --file kit.enc --show-secrets

# Leer solo el instructivo embebido
recovery-kit inspect --file kit.enc --instructions

# Abrirlo sin recovery-kit, solo con el agente
NAT_BACKUP_PASSPHRASE=… nat-backup-agent restore --file kit.enc --dest ./kit-out
```

Documentos relacionados: `docs/key-rotation.md` (rotar `ENCRYPTION_KEY` sin
perder los datos), `docs/schema.md` (qué guarda cada tabla).
