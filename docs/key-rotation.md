# Rotación de claves

Runbook para rotar los secretos de nat-backup cuando se filtraron (por ejemplo,
un `.env` commiteado a un repositorio público).

---

## 1. Por qué hay que rotar

El `.env` contiene tres secretos, y cada uno se rota de forma distinta:

| Secreto | Qué protege | Cómo se rota |
|---|---|---|
| `ENCRYPTION_KEY` | Clave AES-256-GCM que cifra credenciales y passphrases **en la base de datos** | Requiere migrar los datos: `cmd/rotate-key` |
| `JWT_SECRET` | Firma los tokens de sesión de la UI | Cambiar la variable + reiniciar |
| `ADMIN_PASSWORD` | Contraseña del usuario `admin` | Cambiar la variable + borrar la fila + reiniciar |

**`ENCRYPTION_KEY` no es una variable más.** Si la cambiás en el `.env` sin
migrar los datos, todo lo cifrado en la base queda ilegible: los jobs fallan al
despachar, las notificaciones dejan de andar y **los backups ya encriptados con
la passphrase vieja no se pueden restaurar** (la passphrase queda irrecuperable).
No hay forma de deshacerlo sin la clave vieja.

Lo que está cifrado con `ENCRYPTION_KEY`:

- `storage_destinations.config` — credenciales de S3 y SFTP.
- `backup_jobs.encrypt_passphrase` — passphrase de los archivos de backup.
- `notification_settings.config` — credenciales SMTP (en instalaciones viejas
  puede estar en texto plano; la herramienta lo detecta y no lo rompe).

Si además la clave filtrada estuvo expuesta, asumí que esas credenciales
también lo están: **rotá las credenciales de S3/SFTP/SMTP en sus respectivos
proveedores**, no alcanza con re-cifrarlas.

> El historial de git sigue teniendo el `.env`. Rotar las claves es lo urgente;
> limpiar el historial (`git filter-repo`, o borrar y recrear el repo) es
> deseable pero no reemplaza la rotación: hay que asumir que los valores viejos
> ya están en manos de terceros.

---

## 2. Generar las claves nuevas

```bash
# ENCRYPTION_KEY: exactamente 64 caracteres hex (32 bytes)
openssl rand -hex 32

# JWT_SECRET
openssl rand -base64 32

# ADMIN_PASSWORD
openssl rand -base64 24
```

Guardalas en un gestor de contraseñas antes de seguir. **La clave vieja también**:
si algo sale mal, es lo único que puede recuperar los datos.

---

## 3. Rotar `ENCRYPTION_KEY` (orden exacto)

### Paso 1 — Parar el servidor

```bash
sudo systemctl stop nat-backup    # o como corras el binario
```

No es opcional. La herramienta corre en una transacción `SERIALIZABLE` y bloquea
las filas, pero un servidor vivo puede insertar un destino nuevo mientras corre
la rotación y dejarlo cifrado con la clave vieja.

### Paso 2 — Backup de la base de datos (ANTES de tocar nada)

```bash
pg_dump "$DATABASE_URL" > ~/nat-backup-pre-rotacion-$(date +%F-%H%M).sql
ls -lh ~/nat-backup-pre-rotacion-*.sql   # verificá que no esté vacío
```

Este es el único paso que no se puede saltear. Sin este dump, un error en la
rotación no tiene vuelta atrás.

### Paso 3 — Compilar la herramienta

```bash
cd /ruta/al/repo
go build -o bin/rotate-key ./cmd/rotate-key
```

### Paso 4 — Dry run (no escribe nada)

Las claves se pasan por variables de entorno, nunca por flags: un flag queda
visible en `ps` para cualquier usuario de la máquina y en el historial del shell.

```bash
export DATABASE_URL="postgres://usuario:password@localhost:5432/nat_backup?sslmode=disable"
export OLD_ENCRYPTION_KEY="<el ENCRYPTION_KEY actual del .env>"
export NEW_ENCRYPTION_KEY="<el que generaste con openssl rand -hex 32>"

./bin/rotate-key
```

> Poné un espacio delante de los `export` si tu shell tiene
> `HISTCONTROL=ignorespace`, o usá `read -s OLD_ENCRYPTION_KEY` para no dejarlas
> en `~/.bash_history`.

El dry run es el modo por defecto: escribir requiere `--commit` explícito. Leé la
salida antes de seguir:

- `rotated` — se van a re-cifrar. Es lo normal.
- `already encrypted with the new key` — ya migradas (por una corrida anterior).
  Se saltean.
- `plaintext (legacy row, left untouched)` — filas sin cifrar de instalaciones
  viejas. Se dejan como están; para cifrarlas, volvé a guardar esa configuración
  desde la UI después de la rotación.
- `UNDECRYPTABLE` — **freno**. No descifra ni con la clave vieja ni con la nueva.
  Casi siempre significa que `OLD_ENCRYPTION_KEY` no es la clave correcta. No
  sigas: revisá la clave (ver sección 5).

Si el dry run termina sin errores, todas las filas ya fueron descifradas con la
clave vieja, re-cifradas con la nueva y verificadas — en memoria, sin escribir.

### Paso 5 — Commit

```bash
./bin/rotate-key --commit
```

Todo ocurre dentro de una única transacción: se escribe, se releen las filas y se
descifran con la clave nueva, y recién ahí se hace `COMMIT`. Si algo falla en
cualquier punto, hay `ROLLBACK` completo y la base queda **exactamente** como
estaba. No existe el estado "la mitad con una clave y la mitad con la otra".

### Paso 6 — Actualizar el `.env`

```bash
# ENCRYPTION_KEY = el valor de NEW_ENCRYPTION_KEY
nano .env
```

Entre el paso 5 y este, la base y el `.env` no coinciden y el servidor no
funciona. Hacelo inmediatamente después.

Aprovechá y cambiá también `JWT_SECRET` y `ADMIN_PASSWORD` (sección 4).

### Paso 7 — Arrancar y verificar

```bash
sudo systemctl start nat-backup
sudo journalctl -u nat-backup -f
```

Verificá, en este orden:

1. El servidor arranca sin errores de configuración.
2. Podés entrar a la UI con la contraseña nueva.
3. Un job de prueba (`Run now`) termina en `success` — eso prueba que las
   credenciales de storage se descifran bien.
4. La página de Settings muestra la configuración de notificaciones — eso prueba
   que el SMTP se descifra bien.

### Paso 8 — Limpiar

```bash
unset OLD_ENCRYPTION_KEY NEW_ENCRYPTION_KEY
```

Guardá el dump del paso 2 hasta haber verificado todo. Después borralo: contiene
todos los secretos cifrados con la clave vieja, que es pública.

---

## 4. Rotar `JWT_SECRET` y `ADMIN_PASSWORD`

`cmd/rotate-key` **no** los toca. No hay datos que migrar.

### `JWT_SECRET`

Cambiar el valor en el `.env` y reiniciar. Efecto: todas las sesiones abiertas
en el navegador quedan invalidadas y hay que volver a loguearse. Nada más.

### `ADMIN_PASSWORD`

Cambiar la variable **no alcanza**. El servidor crea el usuario admin al arrancar
con `INSERT ... ON CONFLICT (username) DO NOTHING`, así que si la fila ya existe
la ignora y la contraseña vieja sigue siendo válida. Hay que borrar la fila:

```bash
psql "$DATABASE_URL" -c "DELETE FROM admin_users WHERE username = 'admin';"
```

Después actualizá `ADMIN_PASSWORD` en el `.env` y reiniciá: el servidor recrea el
usuario con la contraseña nueva.

### Claves de los agentes — no se ven afectadas

`agents.api_key` guarda un hash bcrypt, no está cifrado con `ENCRYPTION_KEY`.
Rotarla no afecta a ningún agente: siguen conectados y funcionando, no hay que
reinstalar nada.

(Si querés rotarlas igual — porque sospechás que se filtraron por otra vía — se
hace desde la UI, regenerando la key del agente y actualizando su config. Es un
procedimiento distinto y no lo cubre este documento.)

---

## 5. Si algo falla

### El dry run reporta `UNDECRYPTABLE`

La causa más probable es que `OLD_ENCRYPTION_KEY` no sea la clave con la que se
cifraron esos datos. Verificalo antes de cualquier otra cosa:

- Mirá el `.env` que está usando el servidor **en producción**, no una copia
  local ni el `.env.example`.
- Si el proceso corre bajo systemd, la variable puede venir de un
  `EnvironmentFile` distinto: `systemctl show nat-backup -p EnvironmentFile`.
- Revisá el historial de git del `.env` filtrado: puede haber varias claves y la
  correcta ser una anterior.

Si confirmaste que la clave es la correcta y algunas filas siguen sin descifrar,
esas filas están corruptas: ya son ilegibles hoy, con o sin rotación. En ese caso
`--commit --skip-undecryptable` rota todo lo demás y las deja intactas. Después
hay que recrear a mano esos destinos o passphrases desde la UI.

### La herramienta aborta a mitad de camino

No hay nada que reparar. Todo corre en una transacción: si aborta, hubo
`ROLLBACK` y la base quedó como estaba. Corregí lo que reporte el error y volvé a
correr el dry run.

### Se cambió el `.env` sin haber migrado los datos

Síntoma: el servidor arranca pero todos los jobs fallan y las notificaciones no
salen.

Solución: poné de vuelta el `ENCRYPTION_KEY` **viejo** en el `.env`, reiniciá, y
recién ahí seguí este runbook desde el paso 1. Nada se perdió mientras tengas la
clave vieja.

### Se perdió la clave vieja

Los datos cifrados son irrecuperables. Restaurá el dump previo si lo tenés y la
clave de ese momento; si no, hay que recrear a mano cada destino de storage y
cada passphrase, y los backups ya encriptados con la passphrase perdida no se
pueden restaurar.

### Correr la herramienta dos veces

Es seguro. La segunda corrida detecta que todo está cifrado con la clave nueva,
reporta `already encrypted with the new key` y no escribe nada.

---

## 6. Qué NO cubre esta herramienta

- **`backup_jobs.source` se guarda en texto plano**, incluyendo connection
  strings de bases de datos con su contraseña. Rotar `ENCRYPTION_KEY` no lo
  protege: cualquiera con acceso a la base los lee. Si estuvieron expuestos,
  rotá esas contraseñas en el motor correspondiente.
- **No rota credenciales de terceros.** Re-cifrar la clave de acceso de S3 no la
  invalida en AWS. Si el `.env` o la base estuvieron expuestos, rotá las
  credenciales de S3, SFTP y SMTP en sus proveedores.
- **No limpia el historial de git.** El `.env` filtrado sigue ahí.
- **No rota `JWT_SECRET` ni `ADMIN_PASSWORD`** (sección 4).
- **No toca las API keys de los agentes** (bcrypt, no afectadas).
- **No hace el backup de la base por vos** (paso 2).
- **No detecta filas nuevas creadas mientras corre.** Por eso el paso 1 es parar
  el servidor.
