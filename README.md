# Negra Backup

Gestor de backups self-hosted. Un servidor central orquesta agentes que corren en tus máquinas — los agentes ejecutan los backups y suben los archivos a los destinos de almacenamiento configurados.

## Características

- **Backups de archivos** — tar + compresión (gzip o zstd) + cifrado AES-256-GCM opcional
- **Backups de bases de datos** — PostgreSQL, MySQL, SQLite, MongoDB
- **Backends de almacenamiento** — sistema de archivos local, S3-compatible, SFTP
- **Programación cron** — expresiones cron estándar por trabajo
- **Retención** — limpieza automática de ejecuciones antiguas
- **Notificaciones por email** — ante fallas de backup
- **UI web** — dashboard, agentes, trabajos, historial de ejecuciones con logs en vivo
- **Logs en tiempo real** — stream por WebSocket mientras corre un backup

## Cómo funciona

```
┌─────────────────┐        WebSocket         ┌─────────────────┐
│   nat-backup    │ ◄──── agente conecta ──── │  nat-backup     │
│   servidor      │ ──── despacha trabajo ──► │  agente         │
│  (PostgreSQL)   │ ◄──── progreso/listo ───── │  (en el host)   │
└─────────────────┘                           └─────────────────┘
        │                                             │
   UI web (React)                          backup → almacenamiento
```

Los agentes se conectan de forma saliente al servidor — no se necesitan reglas de firewall entrantes en los hosts de los agentes.

## Instalación

### Linux (servidor + agente)

```bash
curl -fsSL https://raw.githubusercontent.com/felipendelicia/negra-backup/main/scripts/install.sh | bash
```

Solo el agente (en máquinas que no corren el servidor):

```bash
curl -fsSL https://raw.githubusercontent.com/felipendelicia/negra-backup/main/scripts/install.sh | bash -s -- --agent-only
```

Si ya está instalado, el script detecta la versión actual y actualiza solo si hay una versión nueva.

### Windows (agente)

En PowerShell como **Administrador**:

```powershell
iwr -useb https://raw.githubusercontent.com/felipendelicia/negra-backup/main/scripts/install-agent.ps1 | iex
```

El script agrega `%ProgramFiles%\nat-backup` al PATH del sistema automáticamente.

---

## Inicio rápido

### 1. Iniciar el servidor

```bash
# Iniciar Postgres
docker compose up -d postgres

# Configurar
cp .env.example .env
# Editar .env: definir JWT_SECRET, ENCRYPTION_KEY (hex de 64 chars), ADMIN_PASSWORD

export $(cat .env | xargs)
nat-backup-server
```

Abrir `http://localhost:8080` — ingresar con `admin` / tu `ADMIN_PASSWORD`.

### 2. Agregar un agente

En la UI web → **Agentes** → crear agente → copiar la API key.

En la máquina a respaldar:

```bash
cat > agent.yaml <<EOF
server_url: http://tu-servidor:8080
api_key: <pegar-api-key>
EOF

nat-backup-agent agent.yaml

# Instalar como servicio del sistema (Linux/Windows)
nat-backup-agent install
```

### 3. Crear un trabajo

UI web → **Trabajos** → configurar fuente (archivos o base de datos), destino de almacenamiento, horario cron.

## Configuración

### Servidor (variables de entorno)

| Variable | Descripción |
|----------|-------------|
| `DATABASE_URL` | Cadena de conexión a PostgreSQL |
| `JWT_SECRET` | Clave para firmar tokens JWT (mín. 32 chars) |
| `ENCRYPTION_KEY` | Clave hex de 64 chars para cifrar configs de almacenamiento y passphrases en reposo |
| `ADMIN_PASSWORD` | Contraseña inicial del administrador |
| `PORT` | Puerto HTTP (por defecto `8080`) |
| `TLS_ENABLED` | `true` para habilitar TLS |
| `TLS_CERT_FILE` | Ruta al certificado TLS |
| `TLS_KEY_FILE` | Ruta a la clave TLS |

### Agente (`agent.yaml`)

```yaml
server_url: https://tu-servidor.ejemplo.com
api_key: tu-api-key
```

## Compilación

```bash
make build              # servidor + agente
make build-full         # UI + servidor + agente
make build-agent-windows  # compilación cruzada del agente para Windows
```

## Desarrollo

```bash
make dev-up             # iniciar Postgres via Docker
make test               # todos los tests
make test-short         # omitir tests que requieren servicios externos
```

## Destinos de almacenamiento

| Tipo | Campos de configuración |
|------|------------------------|
| `local` | `path` — directorio en el host **del agente** |
| `s3` | `endpoint`, `bucket`, `region`, `access_key`, `secret_key` |
| `sftp` | `host`, `port`, `user`, `password` o `private_key`, `path` |

Al usar almacenamiento `local` con el servidor como destino, el agente sube via `POST /api/upload/{run_id}`.

## Seguridad

- Las configs de almacenamiento y passphrases de backup se cifran en reposo con AES-256-GCM usando `ENCRYPTION_KEY`.
- Los valores descifrados solo se mantienen en memoria y se transmiten por la conexión WebSocket autenticada existente.
- Los agentes se autentican con API keys individuales.
- La UI web usa JWTs de corta duración.
