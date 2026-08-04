# Negra Backup

Gestor de backups self-hosted. Un servidor central orquesta agentes que corren en tus máquinas — los agentes ejecutan los backups y suben los archivos a los destinos de almacenamiento configurados.

## Características

- **Backups de archivos** — tar + compresión (gzip o zstd) + cifrado AES-256-GCM opcional
- **Backups de bases de datos** — PostgreSQL, MySQL, SQLite, MongoDB (se vuelca la base a un archivo temporal y se archiva igual que los archivos)
- **Backends de almacenamiento** — filesystem local del agente, S3-compatible, SFTP
- **Programación cron** — cron estándar de 5 campos o descriptores (`@daily`, `@hourly`, `@every 30m`, …)
- **Retención** — borra runs vencidos y su artefacto en S3/SFTP (ver [Limitaciones conocidas](#limitaciones-conocidas) para `local`)
- **Notificaciones por email** — solo ante `job_failed` reportado por un agente
- **UI web** — dashboard, agentes, trabajos, historial de runs, consola y ajustes
- **Consola en vivo** — los logs del servidor y de los agentes se transmiten por WebSocket a la página Console
- **Restauración offline** — `nat-backup-agent restore` recupera un archivo sin servidor ni base de datos

## Cómo funciona

```
┌─────────────────┐                              ┌─────────────────┐
│   nat-backup    │ ◄──── conecta (WebSocket) ─── │   nat-backup    │
│    servidor     │ ───── despacha trabajo ─────► │     agente      │
│  (PostgreSQL)   │ ◄──── progreso / listo ────── │  (en el host)   │
└─────────────────┘                              └─────────────────┘
        │                                                 │
   UI web (React)                            backup → almacenamiento
```

Los agentes se conectan de forma saliente al servidor (`/ws/agent`) — no se necesitan reglas de firewall entrantes en los hosts de los agentes. El agente reintenta la conexión cada 10 s y manda un heartbeat cada 30 s.

## Instalación

### Linux (servidor + agente)

```bash
curl -fsSL https://raw.githubusercontent.com/felipendelicia/negra-backup/main/scripts/install.sh | sudo bash
```

Solo el agente, ya configurado y registrado como servicio systemd:

```bash
curl -fsSL https://raw.githubusercontent.com/felipendelicia/negra-backup/main/scripts/install.sh \
  | sudo bash -s -- --agent-only --server-url http://TU-SERVIDOR:8080 --api-key TU_API_KEY
```

Opciones de `install.sh`:

| Opción | Efecto |
|--------|--------|
| `--agent-only` | Instala solo `nat-backup-agent` |
| `--server-url <url>` | URL del servidor a la que se conecta el agente |
| `--api-key <key>` | API key del agente. También se puede pasar por la variable `NAT_BACKUP_API_KEY` para que no quede en el historial ni en la lista de procesos |

Con `--server-url` y `--api-key` el script escribe `/etc/nat-backup/agent.yaml` (modo 0600, propiedad de root), registra el servicio `nat-backup-agent` y lo arranca. Reejecutar el instalador es seguro: detecta la versión instalada, reemplaza solo lo que cambió y reinicia el servicio si hizo falta.

### Windows (agente)

En PowerShell como **Administrador**:

```powershell
iwr -useb https://raw.githubusercontent.com/felipendelicia/negra-backup/main/scripts/install-agent.ps1 | iex
```

Con configuración y registro del servicio de Windows:

```powershell
& ([scriptblock]::Create((iwr -useb 'https://raw.githubusercontent.com/felipendelicia/negra-backup/main/scripts/install-agent.ps1').Content)) `
    -ServerUrl 'http://TU-SERVIDOR:8080' -ApiKey 'TU_API_KEY'
```

El script agrega `%ProgramFiles%\nat-backup` al PATH del sistema, escribe `%ProgramData%\nat-backup\agent.yaml` con ACL restringida a SYSTEM y Administradores y, cuando se le pasan `-ServerUrl` y `-ApiKey`, registra el servicio `nat-backup-agent` con arranque automático.

---

## Inicio rápido

### 1. Iniciar el servidor

```bash
# Iniciar Postgres
docker compose up -d postgres

# Configurar
cp .env.example .env
# Editar .env y reemplazar TODOS los placeholders.
# ENCRYPTION_KEY debe ser hex de 64 caracteres: openssl rand -hex 32

set -a && . ./.env && set +a
nat-backup-server
```

Abrir `http://localhost:8080` — ingresar con `admin` / tu `ADMIN_PASSWORD`.

Si falta una variable obligatoria, o `ENCRYPTION_KEY` no es hex de 64 caracteres, o `TLS_ENABLED=true` sin certificado, el servidor **no arranca** y dice exactamente qué falta.

### 2. Agregar un agente

En la UI web → **Agents** → crear agente → copiar la API key (se muestra una sola vez; el servidor guarda solo su hash bcrypt).

En la máquina a respaldar:

```bash
cat > agent.yaml <<EOF
server_url: http://tu-servidor:8080
api_key: <pegar-api-key>
EOF

nat-backup-agent agent.yaml
```

Comandos del agente:

| Comando | Efecto |
|---------|--------|
| `nat-backup-agent [ruta.yaml]` | Corre el agente. Sin argumento usa `./agent.yaml` |
| `nat-backup-agent version` | Imprime la versión del binario |
| `nat-backup-agent install` | **Linux**: escribe `/etc/systemd/system/nat-backup-agent.service` (que arranca con `/etc/nat-backup/agent.yaml`) e imprime el `systemctl enable --now` que hay que correr a mano. **Windows**: solo imprime los comandos `sc create` / `sc start`, no registra nada |
| `nat-backup-agent restore --help` | Restaura un archivo de backup (ver abajo) |

`nat-backup-server version` hace lo mismo para el servidor.

### Restaurar un backup

`nat-backup-agent restore` no necesita servidor ni base de datos: detecta compresión y cifrado a partir del archivo mismo.

```bash
# Desde un archivo local
NAT_BACKUP_PASSPHRASE=secreto nat-backup-agent restore --file backup.tar.zst.enc --dest /tmp/out

# Ver qué contiene sin escribir nada
nat-backup-agent restore --file backup.tar.gz --dest /tmp/out --dry-run

# Bajándolo primero de un destino de almacenamiento (JSON inline o ruta a un archivo)
nat-backup-agent restore --from-storage ./s3.json --name job-run.tar.zst.enc --dest /srv/restore
```

La passphrase se resuelve en este orden: `--passphrase`, luego `$NAT_BACKUP_PASSPHRASE`, luego un prompt interactivo. Preferí la variable de entorno: los flags son visibles en la lista de procesos y el prompt hace eco de lo que escribís.

### 3. Crear un trabajo

UI web → **Jobs** → configurar fuente (archivos o base de datos), destino de almacenamiento y horario cron.

## Configuración

### Servidor (variables de entorno)

| Variable | Obligatoria | Descripción |
|----------|-------------|-------------|
| `DATABASE_URL` | sí | Cadena de conexión a PostgreSQL |
| `JWT_SECRET` | sí | Clave para firmar los JWT de la UI. El código solo exige que no esté vacía; usá al menos 32 bytes (`openssl rand -base64 32`) |
| `ENCRYPTION_KEY` | sí | Clave AES-256-GCM. **Debe ser exactamente 64 caracteres hex (32 bytes)** y se valida al arrancar. Generar con `openssl rand -hex 32` |
| `ADMIN_PASSWORD` | sí | Contraseña del usuario `admin`. Solo se aplica en el **primer** arranque |
| `PORT` | no | Puerto HTTP (por defecto `8080`) |
| `UPLOAD_DIR` | no | Directorio donde `POST /api/upload/{run_id}` deja los archivos (por defecto `./data/uploads`) |
| `TLS_ENABLED` | no | `true`/`false`. Un valor no booleano aborta el arranque |
| `TLS_CERT_FILE` | si `TLS_ENABLED=true` | Ruta al certificado. Traé el tuyo: el servidor no genera ninguno |
| `TLS_KEY_FILE` | si `TLS_ENABLED=true` | Ruta a la clave privada |

> **Rotar `ENCRYPTION_KEY` deja ilegibles todas las configs de destinos y todas las passphrases ya guardadas en la base.** No hay reencriptado automático.

Las migraciones corren solas al arrancar el servidor.

### Agente (`agent.yaml`)

Son las dos únicas claves que el agente parsea, y las dos son obligatorias:

```yaml
server_url: https://tu-servidor.ejemplo.com   # http:// o https://; el agente deriva ws:// o wss://
api_key: tu-api-key
```

## Destinos de almacenamiento

| Tipo | Campos de configuración |
|------|-------------------------|
| `local` | `path` — directorio en el host **del agente** |
| `s3` | `bucket`, `region`, `access_key`, `secret_key`; opcionales `endpoint` (para S3-compatible) y `prefix` (por defecto `nat-backup`) |
| `sftp` | `host`, `port` (**número**), `user`, `path`, `password` **o** `private_key`; opcional `host_key` |

> ### ⚠️ Los destinos S3 y SFTP creados antes de esta versión hay que recrearlos
>
> La UI mandaba `access_key_id`, `secret_access_key`, `username` y `port` como string. El backend espera `access_key`, `secret_key`, `user` y `port` numérico, y descartaba silenciosamente los demás — es decir, **S3 y SFTP nunca funcionaron**: el destino se guardaba bien y fallaba recién al subir.
>
> Borrá y volvé a crear cada destino S3 y SFTP existente. Los destinos `local` no están afectados. La API ahora rechaza campos desconocidos, así que una config mal formada falla con 400 al crearla en vez de romper el backup meses después.

**Verificación de host key en SFTP.** El agente ya no acepta cualquier host key. Verifica contra:

1. el campo `host_key` del destino (formato `authorized_keys`, p. ej. `ssh-ed25519 AAAA...`), si está seteado; o
2. el `~/.ssh/known_hosts` del usuario con el que corre el agente.

Si no hay ninguno de los dos, el upload falla con un error explícito en vez de conectarse a ciegas. Al agente lo suele correr root/SYSTEM: el `known_hosts` que importa es el de ese usuario, no el tuyo.

## API HTTP

Rutas registradas en `internal/api/server.go`:

| Método | Ruta | Auth |
|--------|------|------|
| `GET` | `/healthz` | — |
| `GET` | `/ws/agent` | API key en el mensaje `hello` |
| `GET` | `/ws/console?token=<jwt>` | JWT en query (30 req/min por IP) |
| `POST` | `/api/auth/login` | — (10 req/min por IP) |
| `POST` | `/api/upload/{run_id}` | API key de agente (`Authorization: Bearer`) |
| `GET` `POST` | `/api/agents` | JWT |
| `DELETE` | `/api/agents/{id}` | JWT |
| `POST` | `/api/agents/{id}/update` | JWT |
| `GET` | `/api/agents/{id}/browse` | JWT |
| `GET` `POST` | `/api/storage-destinations` | JWT |
| `PUT` `DELETE` | `/api/storage-destinations/{id}` | JWT |
| `GET` `POST` | `/api/jobs` | JWT |
| `PUT` `DELETE` | `/api/jobs/{id}` | JWT |
| `PATCH` | `/api/jobs/{id}/toggle` | JWT |
| `POST` | `/api/jobs/{id}/run` | JWT |
| `GET` | `/api/runs` | JWT |
| `POST` | `/api/runs/{id}/cancel` | JWT |
| `GET` | `/api/runs/{id}/logs` | JWT |
| `GET` | `/api/runs/{id}/logs/ws` | JWT |
| `GET` `PUT` | `/api/settings/notifications` | JWT |

Todo lo demás (`GET /*`) sirve la SPA embebida, con fallback a `index.html`. El cuerpo de las requests JSON está limitado a 1 MiB; `/api/upload/` queda exento.

## Compilación

```bash
make build                # servidor + agente
make build-full           # UI + servidor + agente
make build-agent-windows  # compilación cruzada del agente para Windows
```

## Desarrollo

```bash
make dev-up             # iniciar Postgres via Docker
make dev-down           # detenerlo
make test               # todos los tests
make test-short         # omitir tests que requieren servicios externos
make lint               # gofmt-check + go vet + lint de la UI
make migrate-up         # aplicar migraciones a mano (el servidor ya las corre al arrancar)
```

## Seguridad

- Las configs de destinos de almacenamiento y las passphrases de backup se cifran en reposo con AES-256-GCM usando `ENCRYPTION_KEY`. Los valores descifrados solo viven en memoria y viajan por la conexión WebSocket autenticada del agente.
- Los agentes se autentican con API keys individuales; la base guarda solo el hash bcrypt.
- La UI usa JWT con 24 h de validez. El `?token=` de los WebSockets se redacta en los logs.
- Los archivos subidos por SFTP y por el backend `local` se escriben con permisos 0600 y con rename atómico, así una transferencia interrumpida no deja un archivo truncado con nombre de backup válido.
- **No** se cifran: `backup_jobs.source` (incluye las connection strings de las bases) ni `notification_settings.config` (incluye la contraseña SMTP). Ver `docs/schema.md`.

## Limitaciones conocidas

Lo que el proyecto **no** hace hoy, verificado contra el código:

- **El progreso por run es binario, no continuo.** `backup.BackupFiles` llama a `OnProgress` una sola vez, con `(100, "done")`, cuando ya terminó de comprimir (`internal/backup/files.go`). Los backups de bases de datos ni siquiera setean el callback. El WebSocket de logs del run está vivo, pero lo único que recibe es esa línea y el resultado final.
- **No se guardan logs históricos de los runs.** No existe ninguna tabla de logs: el hub los reparte en memoria (`Hub.logSubs`). `GET /api/runs/{id}/logs` devuelve solo `{run_id, status, message}` de la fila del run, y `GET /api/runs/{id}/logs/ws` cierra la conexión de entrada si el run ya no está en `running`. Un run terminado no se puede "reproducir".
- **`POST /api/upload/{run_id}` no se usa como destino de almacenamiento.** El endpoint existe, autentica con la API key del agente y valida que el run sea del agente, pero ningún backend lo invoca: `storage.NewBackend` recibe `runID`, `serverURL` y `apiKey` y los descarta. El tipo `local` **siempre** escribe en el filesystem del agente. Es código muerto hasta que se implemente un backend que lo use.
- **La retención no puede borrar artefactos en destinos `local`.** Viven en el filesystem del agente, fuera del alcance del servidor: esos runs se conservan (con el motivo en el log) en vez de borrar la fila y dejar el archivo huérfano. En S3 y SFTP sí se borra el archivo y después la fila.
- **Las notificaciones solo cubren los fallos reportados por un agente.** Se disparan con el mensaje `job_failed`. Los fallos que registra el servidor (agente desconectado, destino ilegible, passphrase indescifrable, error al despachar) quedan como run `failed` en la UI pero **no** mandan mail. Tampoco hay aviso de éxito ni de agente caído.
- **TLS es bring-your-own.** No se genera ningún certificado self-signed: `TLS_ENABLED=true` exige `TLS_CERT_FILE` y `TLS_KEY_FILE` y falla al arrancar si faltan.
- **`nat-backup-agent install` no deja el servicio corriendo.** En Linux escribe el unit file y te dice que corras `systemctl enable --now`; en Windows solo imprime los comandos `sc create`/`sc start`. Para que quede registrado y arrancado usá los instaladores (`install.sh` con `--server-url`/`--api-key`, o `install-agent.ps1` con `-ServerUrl`/`-ApiKey`).
- **`ADMIN_PASSWORD` solo se aplica en el primer arranque** (`INSERT ... ON CONFLICT DO NOTHING`). Cambiar la variable después no cambia la contraseña.
- **Un solo usuario.** No hay alta de usuarios, roles ni multi-tenancy: existe únicamente `admin`.

### Sí funciona (aclaraciones)

- **Notificaciones por email desde la UI**: la configuración se lee de la base en cada alerta, así que guardar los ajustes en **Settings** tiene efecto inmediato, sin reiniciar el servidor.
- **Consola en vivo**: `/ws/console` transmite en tiempo real el log del servidor y las líneas de log de todos los agentes conectados.
- **Cancelar un run**: `POST /api/runs/{id}/cancel` marca el run como `cancelled` y le manda la señal al agente, que aborta el trabajo en curso.
