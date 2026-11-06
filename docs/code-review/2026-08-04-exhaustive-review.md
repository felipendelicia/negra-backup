# Review exhaustivo de nat-backup — 2026-08-04

Repo: `/home/felipe/Documents/Repositories/nat-backup` · Branch: `main` · HEAD: `4de649c`
Método: 7 subagentes read-only en paralelo (api-http, ws-scheduler, backup-storage-crypto, cmd-agent-server, web-ui, ops-ci-docs, gaps-pendientes) + verificación adversarial + consolidación.

---

## 0. Caveat inicial (leer antes de tocar nada)

Este informe sale de un review automatizado con subagentes. Aplican estas limitaciones y son importantes:

- Los **P0/P1 pasaron una verificación adversarial** (lectura del código actual, citas literales, y en algunos casos reproducción con `go test -race`, `go run` o `npx tsc/eslint`), pero **NO todos están probados en ejecución**. No había Postgres levantado, ni agente conectado, ni endpoint S3/SFTP real, ni host Windows.
- Los findings marcados `CONFIRMED` tienen evidencia de ejecución o de comparación de tipos inequívoca. Los `ADJUSTED` fueron corregidos durante la verificación (alguna afirmación del reporte original resultó falsa o exagerada y se recortó). Los `UNVERIFIED` son P2/P3 que se reportan por lectura de código y **están marcados como "sin verificar"** en las tablas.
- **Cada agente fue ciego a la configuración y a los guards fuera de su scope.** Un finding puede estar mitigado por algo que vive en otro archivo (validación HTML5 en la UI, un middleware, un default del toolchain). Donde eso se detectó está anotado, pero no se puede garantizar exhaustividad.
- **Confirmá cada finding contra el código antes de escribir el fix.** Las líneas citadas corresponden a `4de649c`; cualquier cambio posterior las corre.
- Algunas ubicaciones citadas por los agentes resultaron **inexistentes** (p. ej. `Dashboard.tsx:295-296` en un archivo de 169 líneas). En esos casos la tabla lleva la ubicación real verificada, no la original.

### Nota de calibración de severidades

El pase adversarial emitió **99 veredictos** sobre los findings P0/P1: **25 CONFIRMED, 74 ADJUSTED, 0 REFUTED**. Ningún finding P0/P1 fue refutado — es decir, ninguno resultó falso —, pero **74 tenían la severidad inflada** y fueron recortados por el verificador.

Un defecto en el pipeline de consolidación hizo que parte de esos recortes **no se aplicaran automáticamente**: varios verificadores devolvieron el título con el prefijo `[P0][Bug] `, y el matcheo por título exacto falló. Reconciliando a mano contra `journal.jsonl`, el conteo real post-verificación es:

| | P0 | P1 | P2 | P3 |
|---|---:|---:|---:|---:|
| Severidad original de los agentes | 29 | 70 | 79 | 40 |
| Tras el pase adversarial | **10** | **29** | **125** | **54** |

Las severidades de este documento reflejan el criterio del consolidador **después** del dedupe (que fusionó, p. ej., cinco causas independientes de "runs colgados" en un solo finding, justificando mantenerlo alto). Donde el verificador y el consolidador difieren, **el verificador fue más conservador**: tratá los P0 de este informe como "P0 o P1 según cuánto pese el impacto agregado", no como once incidentes críticos independientes.

Los siguientes P0 fueron **re-verificados a mano, uno por uno, contra el código en `4de649c`** y son los de mayor confianza: `.env` en repo público · passphrase borrada al editar job (cadena `Jobs.tsx:204` → `jobs.go:104,117` → `files.go:50` → `main.go:301`) · S3/SFTP rotos por mismatch de json tags (`Storage.tsx:48-71` vs `models.go:109-124`) · `close(ch)` fuera del lock con envío concurrente (`hub.go:73` vs `168/197/233/255`, `hub.go:299` vs `269`, `console.go:100`) · buffer en RAM (`main.go:318`) · retención que loguea `would delete` y borra la fila (`retention.go:45-50`).

---

## 1. Resumen ejecutivo

- **El producto no puede restaurar backups.** `storage.Backend` solo declara `Upload`. No hay `Download`, no hay endpoint, no hay comando, no hay UI. `DecryptStream` existe pero su único caller es un test. Un producto de backup sin restore está funcionalmente incompleto, y como los archivos van cifrados con AES-256-GCM + PBKDF2 y la passphrase vive cifrada en la DB, tampoco hay salida manual razonable.
- **Dos de los tres backends de storage están rotos de fábrica.** La UI manda `access_key_id`/`secret_access_key` (el modelo Go espera `access_key`/`secret_key`) y `username` + `port` como string (el modelo espera `user` y un `int`). Verificado ejecutando `json.Unmarshal` contra los structs reales: S3 queda con credenciales vacías y SFTP falla el unmarshal. **Solo `local` funciona hoy.**
- **Hay tres caminos independientes que producen backups sin cifrar donde el usuario cree que están cifrados**, y uno de ellos se dispara con una acción trivial: editar cualquier campo de un job cifrado desde la UI borra la passphrase de la DB; a partir de ahí los archivos salen en texto plano con extensión `.enc`. Confirmado extremo a extremo (`Jobs.tsx:204` → `jobs.go:104` → `scheduler.go:133` → `files.go:50`).
- **El servidor se puede tirar con un panic desde el browser.** Tres hubs (`ConsoleHub`, `Hub.BroadcastRunLog`, `Hub` de agentes) copian canales bajo lock, sueltan el lock y recién ahí envían, mientras el unsubscriber hace `close(ch)`. Reproducido con el race detector: `panic: send on closed channel` en `hub.go:270` y `hub.go:233`. Corre en goroutines sin `recover`, así que **mata el proceso**. Cerrar la pestaña de Console en el momento equivocado alcanza.
- **Secretos comprometidos y publicados.** `git show 528a1bc:.env` devuelve en claro `JWT_SECRET`, `ENCRYPTION_KEY`, `ADMIN_PASSWORD=[REDACTED_ADMIN_PASSWORD]` y el `DATABASE_URL`; el commit está en `origin/main` de un repo **público**. Agregar `.env` al `.gitignore` no borró el blob. Todo lo cifrado con esa clave debe considerarse comprometido.
- **La retención es un stub que borra datos.** `retention.go` loguea `"retention: would delete storage: %s"` y a continuación hace `DELETE FROM backup_runs`. El archivo nunca se borra y, al desaparecer la fila, se pierde el `storage_path`: cada corrida convierte backups en basura huérfana e irrastreable. Corre todos los días a medianoche en producción.
- **Features que existen en la UI pero no funcionan:** logs en vivo de un run (el WS está detrás de auth por header, el browser no puede mandar headers → 401 siempre), notificaciones por email (el sender se cablea una sola vez al arrancar; configurarlo desde Settings no tiene efecto), progreso en tiempo real (`OnProgress` se invoca una vez, con `100`), self-update en Windows (compila, pero `syscall.Exec` devuelve `EWINDOWS`).
- **El agente no escala.** Bufferiza el archivo completo — tar + compresión + cifrado — en un `bytes.Buffer` en RAM antes de subir. Un backup más grande que la RAM del host mata el agente por OOM y deja el run colgado en `running` para siempre. La spec prometía streaming y multipart; no hay ni uno ni otro.
- **Nada de esto lo hubiera atajado el pipeline: no hay CI.** El único workflow se dispara con tags `v*` y va de checkout a publicar binarios sin correr `go test`, `go vet`, `gofmt` ni `npm run lint`. Hoy `gofmt -l` reporta 4 archivos y eslint 11 errores sin que nada falle.

Lo que sí está bien: la arquitectura general (server/agent/SPA, hub WS, cron) es sensata; `internal/crypto` usa AES-256-GCM correctamente para lo que cubre; el schema de DB es limpio y está documentado; los storage destinations y las passphrases sí se cifran en reposo; el chequeo de IDOR en `upload.go` está bien escrito (aunque sin tests y sobre un endpoint muerto).

---

## 2. Conteo por severidad y área

Findings crudos reportados por los agentes (antes de deduplicar):

| Área | P0 | P1 | P2 | P3 | Total |
|---|---:|---:|---:|---:|---:|
| api-http | 5 | 8 | 10 | 7 | 30 |
| ws-scheduler | 6 | 10 | 6 | 2 | 24 |
| backup-storage-crypto | 1 | 7 | 22 | 9 | 39 |
| cmd-agent-server | 1 | 3 | 20 | 9 | 33 |
| web-ui | 3 | 1 | 21 | 13 | 38 |
| ops-ci-docs | 1 | 2 | 16 | 9 | 28 |
| gaps-pendientes | 2 | 5 | 15 | 4 | 26 |
| **Total crudo** | **19** | **36** | **110** | **53** | **218** |
| **Único tras dedupe** | **11** | **24** | **~99** | **~50** | **~184** |

Las 8 fusiones de P0/P1 más relevantes:
- Passphrase borrada al editar job (api-http + web-ui)
- Config S3/SFTP rota (web-ui ×2 + gaps)
- Backup bufferizado en RAM (cmd-agent-server + backup-storage-crypto + gaps)
- Runs colgados en `running` (ws-scheduler + api-http + cmd-agent-server ×2 + gaps)
- `send on closed channel` (api-http + ws-scheduler ×3)
- WS de logs 401 (api-http + ws-scheduler)
- SMTP en claro (api-http + cmd-agent-server + web-ui + ops-ci-docs + gaps)
- bcrypt scan de `agents` (api-http + ws-scheduler + ops-ci-docs)

---

## 3. Findings P0

### [P0][Security] `.env` con secretos reales commiteado y pusheado a un repo público — sigue en el historial

**Location:** blob `e19de21` en commit `528a1bc`; removido del working tree recién en `d135e0e`
**Problem:** `git show 528a1bc:.env` devuelve en claro `JWT_SECRET=[REDACTED_JWT_SECRET]`, `ENCRYPTION_KEY=4df0db76...ece4` (la clave AES-256-GCM que protege TODAS las credenciales de storage y las passphrases de backup), `ADMIN_PASSWORD=[REDACTED_ADMIN_PASSWORD]` y el `DATABASE_URL` con password. `git branch -r --contains 528a1bc` → `origin/main`; la API de GitHub reporta el repo como `"visibility": "public"`. Agregar `.env` al `.gitignore` (`.gitignore:7`) no borra el blob: sigue siendo alcanzable en cualquier clon o fork.
**Proposal:** Rotar **ya**: `JWT_SECRET`, `ADMIN_PASSWORD`, el password de Postgres y `ENCRYPTION_KEY`. La rotación de `ENCRYPTION_KEY` exige re-cifrar `storage_destinations.config` y `backup_jobs.encrypt_passphrase` con la clave nueva (script de migración de datos, no una migración SQL). Después purgar el blob del historial con `git filter-repo`/BFG y force-push. Asumir comprometido todo lo que estaba cifrado con la clave vieja: credenciales S3/SFTP y passphrases de backup.
**Effort:** M · **Risk:** high · **Verificado:** CONFIRMED (`git show`, `git branch -r --contains`, API de GitHub)

---

### [P0][Bug] Editar un job cifrado borra la passphrase y los backups siguientes salen sin cifrar

**Location:** `web/src/pages/Jobs.tsx:204,223-224` · `internal/api/jobs.go:100-121` · `internal/scheduler/scheduler.go:133` · `internal/backup/files.go:50`
**Problem:** Cadena completa confirmada. `openEdit` precarga `encrypt_passphrase: ''` y `handleSubmit` solo borra el campo del payload cuando `!data.encrypt` — así que editar el cron de un job cifrado manda `{encrypt: true, encrypt_passphrase: ""}`. En el server, `handleUpdateJob` solo arma `encPassphrase` si `req.Encrypt && req.EncryptPassphrase != ""`, pero **siempre** escribe `encrypt_passphrase=$11` con ese valor → la columna queda `NULL`. Después `scheduler.go:133` (`if job.Encrypt && job.EncryptPassphrase != nil`) deja `passphrase = ""` y despacha igual, y `files.go:50` (`if cfg.Encrypt && cfg.Passphrase != ""`) cae al branch de texto plano. El agente igual le pone `.enc` al nombre (`cmd/agent/main.go:300-302`) y el run se reporta `success`. Resultado: **archivo sin cifrar, con nombre `.enc`, reportado como backup cifrado exitoso**, y los archivos anteriores quedan irrecuperables desde la UI porque la passphrase se perdió.
**Proposal:** Tres capas, las tres necesarias. (1) UI: omitir `encrypt_passphrase` del payload salvo que el usuario haya tipeado algo, y rotular el campo "Leave blank to keep current passphrase". (2) API: `EncryptPassphrase *string` en `createJobRequest` y `encrypt_passphrase = COALESCE($11, encrypt_passphrase)`; rechazar con 400 `encrypt=true` sin passphrase existente ni nueva. (3) `backup.BackupFiles` debe devolver error si `Encrypt && Passphrase == ""` en vez de degradar silenciosamente.
**Effort:** S · **Risk:** med · **Verificado:** CONFIRMED (cadena completa citada línea por línea por dos agentes independientes)

---

### [P0][Security] Passphrase vacía produce un backup SIN cifrar con nombre `.enc`

**Location:** `internal/backup/files.go:50` (raíz) · `internal/scheduler/scheduler.go:132-143` · `cmd/agent/main.go:300-302`
**Problem:** Mismo síntoma que el anterior pero **otra vía de entrada**, por eso va separado: `scheduler.go` deja `passphrase` vacía cada vez que `job.EncryptPassphrase` es `nil` **o cuando `crypto.Decrypt` falla** (por ejemplo tras cambiar `ENCRYPTION_KEY`), y despacha igual — solo loguea el error de decrypt. `files.go:50` cae al branch de plaintext (`TarCompress` directo a `w`) y el agente igual concatena `.enc`. El fallo de descifrado, que es la señal más fuerte de que algo está mal, se convierte en un backup en claro reportado como exitoso.
**Proposal:** `BackupFiles` devuelve error con `Encrypt && Passphrase == ""`. `scheduler.triggerJob` aborta y marca el run `failed` con el error concreto cuando falla el decrypt de la passphrase de un job con `encrypt=true`, en vez de despachar. Adicionalmente validar `ENCRYPTION_KEY` al arrancar (ver P2 correspondiente) para que el modo degradado no exista.
**Effort:** S · **Risk:** low · **Verificado:** CONFIRMED

---

### [P0][Bug] Los backends S3 y SFTP están rotos: la UI manda nombres y tipos de campo que los modelos Go no aceptan

**Location:** `web/src/pages/Storage.tsx:48-71` · `internal/models/models.go:109-124` · `internal/storage/interface.go:43` · `web/src/lib/types.ts:80`
**Problem:** Reproducido ejecutando `json.Unmarshal` contra los structs reales con los payloads exactos que arma la UI:
- **S3:** la UI escribe `access_key_id` / `secret_access_key` / `prefix`; `S3StorageConfig` espera `access_key` / `secret_key` y no tiene `prefix`. `json.Unmarshal` no falla, descarta las claves desconocidas → `cfg={Bucket:b Region:r AccessKey: SecretKey:}`. **Todo destino S3 creado desde la UI tiene credenciales vacías** y cada upload muere con 403 en tiempo de ejecución.
- **SFTP:** la UI escribe `username` (el modelo espera `user`) y `port` como string desde un `<Input>` de texto; el modelo lo declara `int` → `err=json: cannot unmarshal string into Go struct field SFTPStorageConfig.port of type int`. **Todo backup SFTP aborta** en `storage.NewBackend`.
- La UI tampoco ofrece `private_key` para SFTP, prometido en la spec y en `README.md:142`.
- `CreateStorageRequest.config` es `Record<string, unknown>`, así que TypeScript no ve nada.
**Proposal:** Tipar el config por backend en `web/src/lib/types.ts` como union discriminada por `type`, usando **exactamente** los json tags de los structs Go (`access_key`, `secret_key`, `user`, `port` numérico, `private_key`). Coercionar el puerto con `parseInt(...) || 22`. Agregar el campo private key. Validar el shape en `handleCreateStorage`/`handleUpdateStorage` antes de cifrar, en vez de aceptar cualquier JSON opaco.
**Effort:** S · **Risk:** low · **Verificado:** CONFIRMED (probe con `go run` contra los structs reales)

---

### [P0][Bug] El agente bufferiza el backup completo en RAM antes de subir

**Location:** `cmd/agent/main.go:210,237,280,318` · `internal/storage/interface.go:13`
**Problem:** `var buf bytes.Buffer` recibe el tar + compresión + cifrado entero (`backup.BackupFiles(cfg, &buf)` para archivos, misma cosa para dumps de DB) y recién después se sube con `backend.Upload(filename, &buf, int64(buf.Len()))`. La firma de la interfaz — `Upload(filename string, r io.Reader, size int64) error` — exige el tamaño total por adelantado, que es lo que fuerza el buffer. Un backup de decenas de GB hace OOM al agente (más todavía, porque `bytes.Buffer` duplica capacidad al crecer); el run queda `running` para siempre y el archivo no existe. No hay límite de runs concurrentes en el case `MsgTypeRunJob`, así que se multiplica. La spec prometía "Streaming tar + zstd/gzip" y "Chunked multipart upload"; no hay ni uno ni otro (S3 hace un único `PutObject`).
**Proposal:** Dos caminos, el segundo es el correcto. (a) Mínimo: escribir a `os.CreateTemp` con `defer os.Remove` y pasar el `*os.File` + su `Stat().Size()` — es lo que ya hace el path de DB en `main.go:255`. (b) Correcto: `io.Pipe`, `go BackupFiles(cfg, pw)` y pasar `pr` a `Upload`; sacar el parámetro `size` o hacerlo opcional, y en S3 cambiar `PutObject` por `manager.Uploader` (multipart, acepta un `io.Reader` sin longitud). El size reportado se calcula con un `CountingWriter` en el pipe. Sumar además un semáforo que limite runs concurrentes por agente.
**Effort:** M · **Risk:** med · **Verificado:** CONFIRMED

---

### [P0][Bug] Los runs quedan colgados en `running` para siempre — cinco causas, ningún reaper

**Location:** `internal/ws/hub.go:144-172,70-83` · `internal/scheduler/scheduler.go:101-109` · `internal/api/jobs.go:214-253` · `cmd/agent/main.go:104-110,169,323-332` · `cmd/server/main.go:59-62`
**Problem:** El run se inserta con `status='running'` **antes** de saber si se puede ejecutar, y nada lo cierra nunca en cinco escenarios distintos:
1. **Agente offline:** `DispatchJob` hace `log.Printf("dispatch: agent %s not connected")` y **retorna void**; el endpoint responde `201 Created` como si hubiera funcionado.
2. **Buffer de envío lleno:** mismo tratamiento (`hub.go:169-171`).
3. **Fallo al resolver storage:** `handleTriggerJob` responde 500 con el run ya insertado.
4. **El WS cae a mitad del job:** `executeJob` corre con contexto derivado de `context.Background()`, no de la conexión. El backup termina y se sube, pero `writeJSON(job_done)` devuelve `"not connected"` y solo se loguea → **el archivo existe en el storage y nadie lo sabe**.
5. **Restart del server:** `main.go` resetea `agents` a offline pero no toca `backup_runs`.
`grep -rn "'running'"` confirma que los únicos usos son los dos INSERT: **no existe ningún reaper, ni timeout de run, ni watchdog**. `Hub.Unregister` solo toca la tabla `agents`. Consecuencias: historial permanentemente sucio, la UI ofrece "Cancel" sobre algo que no corre, y la retención nunca los purga porque filtra `status IN ('success','failed')`.
**Proposal:** (a) `DispatchJob` devuelve `error`; `handleTriggerJob` valida `hub.IsConnected` y resuelve storage/passphrase **antes** de insertar el run, y responde 409 `"agent is not online"`. (b) Ante cualquier fallo posterior al insert, marcar el run `failed` con `error_message`. (c) En `Unregister`, marcar `failed` todos los runs `running` de los jobs de ese agente. (d) Al arrancar el server: `UPDATE backup_runs SET status='failed', finished_at=NOW(), error_message='Server restarted while running' WHERE status='running'`. (e) Timeout máximo de run configurable. (f) Para el caso 4, bufferear los mensajes terminales en el agente y reenviarlos tras reconectar.
**Effort:** M · **Risk:** med · **Verificado:** CONFIRMED parcial (el caso 4 confirmado por código; el resto por lectura, sin DB para probar en runtime)

---

### [P0][Bug] `panic: send on closed channel` en los tres hubs — tumba el proceso servidor

**Location:** `internal/ws/hub.go:262-274` vs `284-300` (BroadcastRunLog/UnsubscribeRunLogs) · `internal/ws/hub.go:73` vs `168,197,233,255` (Hub.Run vs Dispatch/Browse/Update/Cancel) · `internal/api/console.go:45-56` vs `96-101` (ConsoleHub.Write vs unsubscribe)
**Problem:** El mismo antipatrón replicado tres veces: el emisor copia los canales bajo `RLock`, **suelta el lock** y recién después hace el `select { case ch <- x: default: }`, mientras el desuscriptor borra del mapa, suelta el lock y hace `close(ch)`. Si el close cae en esa ventana, el send va sobre un canal cerrado → panic. **Reproducido con el race detector** (tests temporales vía `go test -race -overlay`, sin escribir en el repo): salida literal `panic: send on closed channel` en `hub.go:270` y en `hub.go:233`. Los tres corren en goroutines sin `recover`: `BroadcastRunLog` en el read-loop del agente, `Dispatch` desde el scheduler, y `ConsoleHub.Write` **desde cualquier `log.Printf` del servidor** porque es el `io.Writer` de `log` (`cmd/server/main.go:66`). `middleware.Recoverer` no los ve. Disparadores triviales: cerrar la pestaña de Console, cerrar el drawer de logs mientras llega un `job_progress`, o que un agente se desconecte justo cuando el cron dispara.
**Proposal:** Un solo fix, aplicado a los tres: **no cerrar el canal desde el desuscriptor**. Sacar la entrada del mapa bajo `Lock` y dejar que el GC lo recoja; el lector sale por su propio `done`/ctx. Alternativa igual de válida: mantener el `RLock` tomado durante el fan-out (los sends ya son no bloqueantes con `default`, no hay riesgo de deadlock) y hacer el `close` bajo `Lock` exclusivo. Extraer además un `func (h *Hub) sendTo(agentID string, msg ServerMessage) error` para que las cuatro copias del patrón en `hub.go:144-260` no se arreglen a medias.
**Effort:** S · **Risk:** med · **Verificado:** CONFIRMED con `-race`

---

### [P0][Bug] El WebSocket de logs en vivo es inalcanzable desde el browser: siempre 401

**Location:** `internal/api/server.go:162` · `internal/api/middleware.go:20-24` · `internal/api/runs.go:114-158` · `web/src/pages/Runs.tsx:49`
**Problem:** `/api/runs/{id}/logs/ws` está dentro del grupo con `jwtAuthMiddleware`, que exige el header `Authorization: Bearer`. La API WebSocket del browser **no permite setear headers**, y `Runs.tsx:49` abre la URL sin token ni query param. El upgrade nunca ocurre: la feature de logs en tiempo real — publicitada en `README.md:15` — está muerta en producción, el drawer se queda en `○ connecting…` para siempre. `/ws/console` ya resuelve el mismo problema con `?token=`.
**Proposal:** Sacar la ruta del grupo JWT y autenticarla con `?token=` validando con `s.validateJWTString` (mismo patrón que `handleConsoleWS`), y adjuntar el token desde `Runs.tsx`. Alternativa más limpia: `Sec-WebSocket-Protocol` como portador. En cualquier caso, **aplicar antes el fix del JWT en query string** (P0 siguiente) para no multiplicar la superficie, y validar que el run exista antes de suscribir.
**Effort:** S · **Risk:** low · **Verificado:** por código (deducción sólida: ruta en el grupo + middleware solo lee header + cliente sin token)

---

### [P0][Security] Sin control de propiedad: cualquier agente puede cerrar o falsear el run de cualquier otro

**Location:** `internal/ws/handler.go:136-167`
**Problem:** `job_done`, `job_failed` y `job_progress` usan `msg.RunID` tal cual, sin verificar que ese run pertenezca a un job del agente autenticado: `UPDATE backup_runs SET status='success', size_bytes=$1, file_count=$2, storage_path=$3 WHERE id=$4`. Un agente comprometido —o simplemente uno con un bug de `run_id`— puede marcar como exitoso el backup fallido de otro host, falsear `size_bytes`/`storage_path` e inyectar líneas en el stream de logs de runs ajenos. Es la única autorización a nivel recurso que falta en todo el sistema (la de `upload.go` sí está, aunque sin tests).
**Proposal:** Scopear todos los UPDATE por agente: `... WHERE id=$N AND job_id IN (SELECT id FROM backup_jobs WHERE agent_id=$agent)`. En `job_progress`, validar la misma pertenencia antes de `BroadcastRunLog`. Loguear y descartar los mensajes cuyo `run_id` no pertenezca al agente. Validar además que `RunID` sea un UUID antes de usarlo.
**Effort:** M · **Risk:** med · **Verificado:** por código

---

### [P0][Security] Credenciales SMTP en texto plano en la DB y devueltas por la API al browser

**Location:** `internal/api/settings.go:12-20,22-49` · `internal/models/models.go:127-135` · `web/src/pages/Settings.tsx:46,185-186` · `migrations/002_storage_config_text.up.sql:3`
**Problem:** `handleUpdateNotificationSettings` inserta `body.Config` crudo en `notification_settings.config` **sin pasar por `crypto.Encrypt`**, y `EmailNotificationConfig.Password` tiene tag `json:"password,omitempty"` (no `json:"-"`). `handleGetNotificationSettings` devuelve el config completo, password incluida, al browser, que la pinta en el input y la mantiene en el heap de JS, en React DevTools, en la respuesta de red y en cualquier cache HTTP. La migración 002 convirtió esa columna a TEXT **explícitamente para almacenar ciphertext AES** y el cifrado nunca se implementó. Contrasta con `storage_destinations`, que sí se cifra y cuyo config se excluye a propósito del listado (`storage.go:23`), y contradice `README.md:148`, que afirma que las configs se cifran en reposo.
**Proposal:** Cifrar el config con `crypto.Encrypt` antes del INSERT y descifrar solo al construir el `EmailSender`. En el GET, devolver el config con la password omitida o un flag `password_set: true`. En el PUT, preservar la existente si llega vacía. Cambiar el tag a `json:"-"` para que no pueda volver a filtrarse por accidente. Actualizar la UI para mandar la password solo cuando el usuario tipea una nueva.
**Effort:** M · **Risk:** med · **Verificado:** por código, reportado por 5 agentes

---

### [P0][Security] El JWT viaja en query string y termina escrito en los logs y re-emitido por el Console

**Location:** `internal/api/console.go:122-127` · `internal/api/server.go:115` · `cmd/server/main.go:66` · `web/src/pages/Console.tsx:32`
**Problem:** `handleConsoleWS` toma el token de `?token=`. El router monta `middleware.Logger`, que imprime `r.RequestURI` (chi v5.3.0 `middleware/logger.go:120`), o sea **el JWT completo**. Ese log va por `io.MultiWriter` a `os.Stderr` y al propio `ConsoleHub`, así que el token se persiste en el ring buffer de 500 líneas y se **re-emite a cualquier suscriptor del Console**, además de quedar en access logs de proxies, en el historial del browser y en headers `Referer`. El token es el de sesión, con TTL de 24 h y sin revocación posible.
**Proposal:** Reemplazar el query param por un ticket de un solo uso y corta vida emitido por un endpoint autenticado (`POST /api/ws-ticket`), o por `Sec-WebSocket-Protocol`. Como mínimo inmediato, un middleware que redacte el valor de `token` antes de loguear la URI. A más largo plazo, mover el token de sesión a una cookie `httpOnly; SameSite`.
**Effort:** M · **Risk:** med · **Verificado:** por código

---

## 4. Findings P1

### [P1][MissingFeature] No existe restauración de backups en ningún punto del sistema

**Location:** `internal/storage/interface.go:11-14` · `internal/api/server.go:113-169` · `internal/backup/encrypt.go:85`
**Problem:** La interfaz `Backend` solo declara `Upload(filename, r, size)`. No hay `Download`, `List` ni `Delete`. No hay ruta `/api/runs/{id}/download` ni `/api/restore*`, ni cliente en `web/src/lib/api.ts`, ni comando en el agente. `grep -rn "DecryptStream"` sobre el árbol no-test devuelve solo la definición: el único caller es `encrypt_test.go`. `grep -rni "restore|download"` sobre `web/src` no devuelve nada. Los archivos van cifrados con AES-256-GCM + PBKDF2, sin header de formato documentado, y la passphrase solo existe cifrada en la DB: un usuario con el `.enc` y la passphrase **no tiene forma soportada de recuperar sus datos**.
**Proposal:** Corto plazo (desbloquea el producto): comando `nat-backup-agent restore <archivo> --passphrase` que haga reader → `DecryptStream` → zstd/gzip → extracción de tar con chequeo de path traversal en los nombres de entrada, y documentar el formato en `docs/`. Mediano plazo: agregar `Download(filename) (io.ReadCloser, error)` y `List(prefix)` a `storage.Backend`, implementarlos en los tres backends, exponer `GET /api/runs/{id}/download` que despache un `restore_job` al agente, y un botón Restore/Download en `Runs.tsx`.
**Effort:** L · **Risk:** high · **Verificado:** ADJUSTED (grep exhaustivo)

---

### [P1][Bug] La retención borra la fila de la DB pero nunca el archivo del storage

**Location:** `internal/notify/retention.go:46-53,11-14`
**Problem:** El código ejecuta `log.Printf("retention: would delete storage: %s", *storagePath)` y acto seguido `DELETE FROM backup_runs WHERE id=$1`. El archivo en local/S3/SFTP queda para siempre y, como se borró la única fila que guardaba su `storage_path`, **ni siquiera es rastreable**: cada ciclo convierte backups en basura huérfana que ocupa espacio y costo indefinidamente. Es un stub de desarrollo corriendo en producción todos los días a medianoche (`cmd/server/main.go:89-90`). El struct ni siquiera tiene forma de llegar al storage: `type RetentionCleaner struct { db *sqlx.DB; sender *EmailSender }` — sin backend ni encryption key. La spec (`design.md:264-267`) y `README.md:11` prometen limpieza automática.
**Proposal:** Agregar `Delete(filename) error` a `storage.Backend` e implementarlo en los tres backends. En `RetentionCleaner.Run()`: resolver el `storage_destination_id` del job, descifrar su config, borrar el objeto, y **solo entonces** borrar la fila. Si el borrado remoto falla, no borrar la fila (reintento en la próxima corrida) y marcarla para diagnóstico. Nota: el borrado en el storage del agente requiere despacharlo por WS, no se puede hacer desde el server.
**Effort:** L · **Risk:** med · **Verificado:** CONFIRMED

---

### [P1][Bug] Editar un storage destination borra su config cifrada (pérdida de credenciales)

**Location:** `web/src/pages/Storage.tsx:129` · `internal/api/storage.go:24,71-79`
**Problem:** `openEdit` inicializa el formulario con `config: {}` porque `GET /api/storage-destinations` excluye deliberadamente la columna `config` y **no existe un `GET /api/storage-destinations/{id}`**. `handleUpdateStorage` cifra y persiste lo que venga en el body **sin merge y sin guard de config vacía**. Renombrar un destino sobrescribe su config con `{}`: se pierden bucket, claves S3 y credenciales SFTP en silencio, y todos los jobs que lo usan empiezan a fallar.
**Proposal:** Agregar `GET /api/storage-destinations/{id}` que devuelva la config descifrada con los secretos enmascarados, y prefill del formulario con eso. En el PUT, hacer merge sobre la config existente y no pisar campos ausentes o enmascarados. Como guard inmediato de bajo costo: rechazar con 400 un `config` vacío en el update.
**Effort:** M · **Risk:** med · **Verificado:** ADJUSTED

---

### [P1][Bug] Las notificaciones por email quedan congeladas al arranque: configurarlas desde la UI no tiene efecto

**Location:** `cmd/server/main.go:69-90` · `internal/api/settings.go:22-56` · `internal/ws/handler.go:45,172-174`
**Problem:** El `EmailSender` se construye **una única vez** leyendo `notification_settings` en el arranque, y `agentHandler.SetFailureNotifier(emailSender)` es su único caller en todo el repo (`grep` confirmado). `handleUpdateNotificationSettings` solo hace DELETE+INSERT en la tabla; nada re-inyecta el sender, y el `Server` ni siquiera tiene referencia al `AgentHandler` desde `settings.go`. En una instalación nueva (sin settings al primer arranque) `emailSender` es `nil` y `notifyFailure` cae para siempre en el branch `"no notifier configured"`. `NewRetentionCleaner(pool, emailSender)` recibe el mismo `nil` congelado. `README.md:12` promete "Notificaciones por email ante fallas de backup".
**Proposal:** Leer la config de notificaciones desde la DB **en el momento de enviar** (dentro de `notifyFailure`), que es lo más simple y elimina la clase entera de bug; o exponer un `notify.Reloader` que `handleUpdateNotificationSettings` invoque tras el commit.
**Effort:** M · **Risk:** low · **Verificado:** CONFIRMED

---

### [P1][Security] La autenticación de agentes corre bcrypt contra TODA la tabla en cada request y en cada handshake

**Location:** `internal/api/middleware.go:60-79` · `internal/ws/handler.go:199-222` · `migrations/001_initial_schema.up.sql:6`
**Problem:** `agents.api_key` guarda un hash bcrypt (`agents.go:146`), así que ningún lookup puede usar el índice: ambos caminos hacen `SELECT id, api_key FROM agents` y corren `bcrypt.CompareHashAndPassword` fila por fila hasta encontrar match. Con cost 10 (~70 ms/hash) y 50 agentes son ~3.5 s de CPU por request, y **una API key inválida siempre fuerza el escaneo completo**. Cualquiera que conozca la URL puede saturar el servidor con POSTs a `/api/upload/{run_id}` o abriendo conexiones a `/ws/agent`. El `UNIQUE` de la columna es decorativo: cada hash bcrypt tiene salt distinto. La latencia de reconexión crece linealmente con la cantidad de agentes, y el backoff fijo de 10 s del agente sincroniza a todos en la misma ventana tras un restart (thundering herd).
**Proposal:** Cambiar el formato de la API key a `<agent_id>.<secret>` para hacer un único `SELECT` por id y un solo bcrypt; o guardar SHA-256 del secreto (la key ya son 32 bytes aleatorios, no necesita KDF) en una columna indexada y comparar con `subtle.ConstantTimeCompare`. Requiere migración con re-emisión de keys o soporte de ambos formatos durante la transición. Sumar rate limiting por IP en ambos endpoints.
**Effort:** M · **Risk:** med · **Verificado:** ADJUSTED

---

### [P1][Bug] Borrar un agente deja sus entradas de cron vivas para siempre

**Location:** `internal/api/agents.go:28-46` · `internal/scheduler/scheduler.go:64-94` · `migrations/001_initial_schema.up.sql:25`
**Problem:** `handleDeleteAgent` hace `DELETE FROM agents` y el FK con `ON DELETE CASCADE` borra `backup_jobs` y `backup_runs`, pero **nunca llama a `s.sched.RemoveJob`**. Las entradas del cron siguen registradas (`entryIDs` solo se limpia vía `RemoveJob`) y en cada tick `triggerJob` intenta `INSERT INTO backup_runs` con un `job_id` inexistente → violación de FK logueada indefinidamente, hasta el próximo restart.
**Proposal:** Antes del DELETE, seleccionar los ids de `backup_jobs` del agente y llamar a `s.sched.RemoveJob(id)` para cada uno, dentro de la misma transacción. Exponer un `RemoveJobsByAgent(agentID)` en el scheduler para hacerlo atómico. Revisar lo mismo si algún día `storage_destinations` pasa a cascadear.
**Effort:** S · **Risk:** med · **Verificado:** por código

---

### [P1][Bug] El walk de tar no filtra tipos de archivo ni tolera cambios de tamaño: cuatro formas de romper un backup entero

**Location:** `internal/backup/compress.go:45-70,73-77`
**Problem:** Cuatro fallos del mismo loop, tres de ellos **reproducidos** con una réplica standalone del walk:
1. **Symlinks:** `tar.FileInfoHeader(info, "")` da `Typeflag=TypeSymlink` con `Size=0` y `Linkname` **vacío**; después `!d.IsDir()` es true y `copyFile` abre el target y escribe bytes en un writer con size 0 → `archive/tar: write too long`. Salida literal: `RESULT: copy /tmp/.../symcase/link.txt: archive/tar: write too long`. **Un solo symlink en cualquier parte del árbol mata todo el backup.**
2. **Sockets:** `FileInfoHeader` devuelve error (`archive/tar: sockets not supported`) que se propaga fuera de `WalkDir`. Un `.sock` en un home o un directorio de app alcanza.
3. **FIFOs:** se les escribe el header y después `os.Open` en `copyFile` **bloquea hasta que aparezca un writer**. Reproducido: `RESULT: HUNG - os.Open on FIFO blocked >5s`. El backup cuelga indefinidamente.
4. **Archivo que cambia de tamaño:** el header se escribe con el size del stat y después se streamea. Si creció (un log activo) → `write too long`; si se achicó → `missed writing N bytes` en el siguiente `WriteHeader`. Reproducido. **Es la condición más común del mundo real en un backup de sistema vivo.**
No hay retry, ni skip-on-error, ni snapshot: cualquiera de las cuatro aborta el run completo.
**Proposal:** Branchear por `info.Mode()`: para symlinks leer el target con `os.Readlink` y pasarlo a `FileInfoHeader`, escribiendo solo el header; **saltear con warning logueado** todo lo que no sea dir, regular o symlink (sockets, devices, FIFOs); y solo llamar a `copyFile` para `info.Mode().IsRegular()`. Para el skew de tamaño, usar `io.CopyN(tw, f, header.Size)` con zero-padding si el read es corto y truncado si es largo, logueando el skew en esa entrada en vez de fallar el archivo entero.
**Effort:** M · **Risk:** low · **Verificado:** CONFIRMED (3 de 4 reproducidos)

---

### [P1][Security] SFTP con verificación de host key deshabilitada — MITM en cada upload

**Location:** `internal/storage/sftp.go:35-37,42` · `internal/models/models.go:117-124`
**Problem:** `HostKeyCallback: ssh.InsecureIgnoreHostKey()` acepta cualquier host key, así que un atacante on-path puede impersonar el destino SFTP y recibir **todos los backups** más, cuando hay auth por password, **la password SFTP en claro** en su servidor. `SFTPStorageConfig` no tiene campo `host_key` ni `known_hosts`, o sea que **ninguna configuración puede mitigarlo**. El código lo marca como TODO con un `//nolint:gosec`.
**Proposal:** Agregar `host_key` (o path a known_hosts) a `SFTPStorageConfig` y usar `ssh.FixedHostKey`/`knownhosts.New`. En el primer connect, mostrar el fingerprint en la UI para que el usuario confirme (TOFU) en vez de aceptarlo a ciegas.
**Effort:** M · **Risk:** med · **Verificado:** ADJUSTED

---

### [P1][Bug] `mysqldump` recibe el connection string como nombre posicional de base de datos

**Location:** `internal/backup/db.go:60-61`
**Problem:** `exec.Command("mysqldump", "--single-transaction", "--routines", "--triggers", d.connString)` — el argumento posicional de mysqldump es `db_name`, **no un DSN**. Un `mysql://user:pass@host/db` hace que mysqldump se conecte a localhost con credenciales default y busque una base llamada literalmente así. **Los backups de MySQL están rotos o apuntando al target equivocado.** Además no hay guard `--` (a diferencia del branch de sqlite en `db.go:77`), así que un conn string que empiece con `-` se convierte en flag (`--result-file=...`, `--defaults-extra-file=...`): **inyección de argumentos desde config almacenada**. La UI alimenta un DSN (`Jobs.tsx:109-110`).
**Proposal:** Parsear el DSN y emitir `--host/--port/--user` explícitos (password vía defaults-file 0600), pasar el nombre de la base al final después de `--`, y aplicar el mismo rechazo de `strings.HasPrefix("-")` que ya tiene sqlite a todos los dumpers.
**Effort:** M · **Risk:** med · **Verificado:** CONFIRMED

---

### [P1][Bug] El "dump" de SQLite es una copia cruda sin lock que puede perder datos del WAL

**Location:** `internal/backup/db.go:80-92`
**Problem:** El `sqlite3 ... 'PRAGMA wal_checkpoint(FULL)'` es best-effort y **su error se descarta explícitamente** — si `sqlite3` no está instalado o el checkpoint está bloqueado por un reader, no pasa nada. El `io.Copy` posterior del archivo principal **no toma ningún lock** y nunca copia `-wal`/`-shm`: con escrituras concurrentes se obtiene una imagen con páginas rotas y se pierden todas las transacciones post-checkpoint. El backup se reporta como exitoso.
**Proposal:** Usar `sqlite3 <db> ".backup '<tmp>'"` (o `VACUUM INTO`), que toma un read lock apropiado y produce un snapshot consistente de un solo archivo. **Fallar el run** si el comando no está disponible o devuelve non-zero, en vez de degradar silenciosamente a una copia cruda.
**Effort:** M · **Risk:** med · **Verificado:** CONFIRMED

---

### [P1][Security] Passphrase y credenciales de storage viajan en claro por `ws://` con la config por defecto

**Location:** `internal/ws/types.go:54-64` · `internal/ws/hub.go:153-160` · `cmd/server/main.go:107-114` · `cmd/agent/main.go:380-393`
**Problem:** `ServerMessage` lleva la `Passphrase` **descifrada** y el `StorageConfig` **descifrado** (claves S3, password SFTP). El transporte solo es `wss` si el operador setea `TLS_ENABLED`, y el agente deriva el esquema del `server_url`. No hay validación ni warning: con la configuración por defecto **todo el secreto del sistema de backups viaja en texto plano por la red**, anulando el cifrado en reposo de `internal/crypto`.
**Proposal:** Rechazar el despacho de jobs con passphrase o `storageConfig` sobre conexiones no-TLS, salvo que se setee explícitamente un `ALLOW_INSECURE_WS=true`; loguear un warning fuerte al arrancar en ese caso. Documentar TLS como requisito en `README.md` y `.env.example`.
**Effort:** M · **Risk:** med · **Verificado:** por código

---

### [P1][Bug] `triggerJob` despacha igual cuando falla el fetch o el decrypt del storage

**Location:** `internal/scheduler/scheduler.go:117-143`
**Problem:** Si el SELECT del `storage_destination` falla o `crypto.Decrypt` falla, **solo se loguea** y la ejecución sigue: se despacha el job con `storageType=""` y `storageConfig=nil`. El agente hace el backup completo (dump, compresión, cifrado), desperdicia I/O y CPU del host, y recién falla al subir con un mensaje confuso; el fallback a `local` sin path ni siquiera puede funcionar (`cmd/agent/main.go:304-316`). Idem con la passphrase, con la consecuencia mucho peor descrita en el P0 correspondiente: se despacha con passphrase vacía y el backup sale **sin cifrar** aunque el job diga `encrypt=true`.
**Proposal:** Return temprano marcando el run como `failed` con el error concreto cuando falla el fetch o el decrypt del storage. Si `job.Encrypt` es true y la passphrase no se pudo descifrar, **abortar**, nunca despachar sin cifrado.
**Effort:** S · **Risk:** low · **Verificado:** por código

---

### [P1][Bug] Sin protección de solapamiento: dos runs del mismo job en paralelo

**Location:** `internal/scheduler/scheduler.go:31,76-78,96-144`
**Problem:** El cron se crea con `cron.New()` sin `cron.WithChain(cron.SkipIfStillRunning(...))`, y `triggerJob` retorna apenas despacha, así que el cron nunca ve el job como "en ejecución". Un backup que tarda más que su intervalo (`*/5` sobre un dump de 20 minutos) acumula runs concurrentes del mismo job, sobre el mismo agente y el mismo destino: archivos pisándose, disco saturado y múltiples runs `running` para el mismo job.
**Proposal:** Envolver el cron con `SkipIfStillRunning` o, mejor y más robusto ante restarts, chequear en `triggerJob` si ya existe un `backup_run` en `running` para ese `job_id` antes de insertar, y saltear con log si lo hay.
**Effort:** S · **Risk:** low · **Verificado:** por código

---

### [P1][Bug] `DeliverBrowseResult` puede bloquear el read-loop de un agente para siempre

**Location:** `internal/ws/hub.go:214-221`
**Problem:** `ch <- BrowseResponse{...}` es un envío **bloqueante** sobre un canal con buffer 1, ejecutado sincrónicamente desde `handleAgentMessage`, o sea desde la goroutine que lee del WS de ese agente. Si el agente manda dos `browse_result` con el mismo `request_id`, el segundo bloquea indefinidamente: el agente **deja de procesar `job_done`/`job_failed`/heartbeat para siempre**, sigue figurando `online` (el reconcile no lo toca porque está en el mapa) y todos sus runs quedan colgados.
**Proposal:** Usar `select { case ch <- ...: default: }` y borrar la entrada de `browseReqs` dentro del mismo lock **antes** de enviar, de modo que cada `request_id` se entregue una sola vez.
**Effort:** S · **Risk:** low · **Verificado:** por código

---

### [P1][Performance] `Hub.Run` hace I/O de base de datos sincrónico dentro del event loop

**Location:** `internal/ws/hub.go:55-85,106-135`
**Problem:** El único `select` del hub ejecuta el `UPDATE agents SET status='offline'` en la rama de unregister y **toda `reconcileOffline`** (un Query + N Execs) en línea. Con la DB lenta o colgada, ningún agente puede registrarse ni desregistrarse: `register`/`unregister` son canales de 16, se llenan, y `Register()` bloquea el handshake de todos los agentes.
**Proposal:** Sacar el I/O del loop: lanzar las actualizaciones de estado en goroutines o delegarlas a un worker con cola propia, y correr `reconcileOffline` en su propia goroutine con contexto y timeout.
**Effort:** S · **Risk:** low · **Verificado:** por código

---

### [P1][Bug] Un unregister viejo marca offline a un agente que ya reconectó

**Location:** `internal/ws/hub.go:70-83`
**Problem:** La rama de unregister protege el mapa con `if current, ok := h.agents[ac.agentID]; ok && current == ac`, pero el `UPDATE agents SET status='offline'` queda **fuera** de ese guard y corre siempre. Si el agente reconecta rápido (el handshake ya puso `status='online'`) y después llega el unregister de la conexión vieja, la DB lo deja offline aunque esté conectado: la UI miente hasta el próximo reconcile (hasta 60 s).
**Proposal:** Mover el UPDATE dentro del bloque `current == ac`.
**Effort:** S · **Risk:** low · **Verificado:** por código

---

### [P1][Security] Ningún WebSocket define `SetReadLimit`: un solo mensaje puede ser ilimitado

**Location:** `internal/ws/handler.go:57,96` · `internal/api/console.go:129-158` · `internal/api/runs.go:121`
**Problem:** `grep -rn SetReadLimit` no devuelve nada en todo el repo. `gorilla/websocket` sin ReadLimit acumula el frame completo en memoria antes de devolverlo: un agente comprometido — o cualquiera que llegue al handshake **pre-auth** en `handler.go:57` — puede mandar un mensaje de varios GB y provocar OOM del servidor.
**Proposal:** `conn.SetReadLimit` con un tope razonable (1 MB para el agente, 4 KB para console y logs) inmediatamente después del Upgrade, **antes** de leer el `hello`.
**Effort:** S · **Risk:** low · **Verificado:** por código (grep)

---

### [P1][Security] Login sin rate limiting ni backoff: fuerza bruta libre contra `ADMIN_PASSWORD`

**Location:** `internal/api/auth.go:18-49` · `internal/api/server.go:113-169`
**Problem:** `POST /api/auth/login` no tiene throttling, lockout, captcha ni delay incremental, y es **el único factor de autenticación de toda la UI** (usuario fijo `admin`, password de env). No hay ningún middleware de rate limit en `buildRouter`. Con bcrypt cost 10 un atacante saca miles de intentos por hora en paralelo, y de paso quema la CPU del servidor. Agravante: `.env.example` propone `ADMIN_PASSWORD=change-me` y no hay forma de cambiar la password después del primer boot (ver gaps).
**Proposal:** `httprate.LimitByIP` (o equivalente) sobre `/api/auth/login` y sobre `/ws/console`, más un contador de fallos por IP con backoff exponencial.
**Effort:** S · **Risk:** low · **Verificado:** por código

---

### [P1][Security] Sin límite de tamaño de request en ningún endpoint: JSON ni upload

**Location:** `internal/api/server.go:113-169` (JSON) · `internal/api/upload.go:74` (multipart)
**Problem:** Todos los handlers hacen `json.NewDecoder(r.Body).Decode(...)` (`auth.go:20`, `agents.go:54`, `jobs.go:45,99,175`, `storage.go:34,67`, `settings.go:27`) **sin `http.MaxBytesReader`** y sin middleware global. Un POST a `/api/auth/login` (pre-auth) con un body de varios GB obliga al servidor a consumirlo. En paralelo, `upload.go` hace `io.Copy(dst, file)` sin cota alguna: `maxUploadMemory` (32 MB) solo controla el buffer en RAM, no el tamaño total. Un agente comprometido — o cualquiera con una API key válida — **llena el filesystem del servidor con un único POST**. Tampoco hay validación contra `retention_days` ni cuota por agente.
**Proposal:** Middleware en `buildRouter` que envuelva `r.Body = http.MaxBytesReader(w, r.Body, 1<<20)` para todas las rutas `/api` salvo el upload, más `dec.DisallowUnknownFields()` donde aplique. Para el upload, `http.MaxBytesReader` con `maxUploadBytes` configurable e `io.CopyN`; ante exceso, borrar el archivo parcial y responder 413.
**Effort:** S · **Risk:** low · **Verificado:** por código

---

### [P1][Bug] Los uploads se guardan en `os.TempDir()` y se registran como ruta de almacenamiento definitiva

**Location:** `internal/api/upload.go:59-82`
**Problem:** El archivo se escribe en `os.TempDir()/nat-backup-uploads/{run_id}` y esa ruta se persiste en `backup_runs.storage_path` como `local://...`. `systemd-tmpfiles` y la limpieza del SO borran `/tmp` periódicamente y en cada reboot: **los backups desaparecen** mientras la UI sigue mostrando el run como `success` con su `storage_path`. Pérdida silenciosa de datos. (Atenuante actual: el endpoint no tiene callers — ver P2 de código muerto — pero eso lo vuelve peor, no mejor: si se reactiva sin arreglar esto, se pierde datos.)
**Proposal:** Directorio de datos configurable (`SERVER_STORAGE_PATH`/`DATA_DIR`) fuera de `/tmp`, validado y escribible al arrancar; fallar el startup si no lo es.
**Effort:** M · **Risk:** med · **Verificado:** por código

---

### [P1][Bug] La expresión cron no se valida: el job se crea con 201 pero nunca ejecuta

**Location:** `internal/api/jobs.go:49-88,132-139,194-199`
**Problem:** `handleCreateJob` solo chequea `ScheduleCron != ""`. Si la expresión es inválida el INSERT pasa, se responde 201 con `enabled:true`, y `s.sched.AddJob` falla — **el error solo se loguea**. El usuario ve un job habilitado que jamás dispara. La UI permite cron libre (opción Custom en `Jobs.tsx:376-390`), así que es alcanzable trivialmente. Mismo agujero en `handleUpdateJob` y `handleToggleJob`.
**Proposal:** Validar con `cron.ParseStandard(req.ScheduleCron)` antes del INSERT/UPDATE y devolver 400 con el error de parseo; propagar el fallo de `sched.AddJob` como 500 en vez de solo loguearlo.
**Effort:** S · **Risk:** low · **Verificado:** por código

---

### [P1][Security] El instalador de Windows escribe la API key en un archivo legible por cualquier usuario local

**Location:** `scripts/install-agent.ps1:21,74-78`
**Problem:** `agent.yaml` se crea en `%ProgramData%\nat-backup` con la ACL heredada, donde `BUILTIN\Users` tiene lectura. Cualquier usuario local lee la API key del agente y puede impersonarlo contra el server. `grep` de `ACL|icacls|SetAccessControl` sobre `scripts/` y `cmd/agent/` no devuelve nada. El equivalente Linux **sí** hace `sudo chmod 600` (`scripts/install.sh:95`), o sea que la omisión es del script de Windows, no una decisión de diseño.
**Proposal:** Tras el `Set-Content`, reescribir la ACL del archivo dejando solo SYSTEM y Administrators (`Get-Acl`/`Set-Acl` con `SetAccessRuleProtection`).
**Effort:** S · **Risk:** low · **Verificado:** CONFIRMED

---

### [P1][DependencyHygiene] ~83 MB de binarios compilados trackeados en el repo

**Location:** `agent`, `server`, `migrate`, `bin/nat-backup-agent`, `bin/nat-backup-agent.exe`, `bin/nat-backup-server`
**Problem:** **Seis** binarios trackeados (`git ls-files -s | grep 100755`), 83.1 MB en total; `.git` pesa 168 MB. El `.gitignore` lista `bin/` pero eso **no afecta archivos ya trackeados**, así que cada rebuild + commit sube otra copia completa y el repo crece sin techo. Además son binarios sin firmar que cualquiera puede bajar del repo creyendo que son el release oficial.
**Proposal:** `git rm --cached` los seis, agregar `/agent`, `/server`, `/migrate` al `.gitignore`, y purgarlos del historial **en la misma pasada de `git filter-repo` que borra el `.env`** (aprovechar el force-push que ya hay que hacer).
**Effort:** M · **Risk:** med · **Verificado:** CONFIRMED

---

### [P1][Testing] Cero cobertura de los caminos concurrentes que efectivamente paniquean

**Location:** `internal/ws/hub_test.go:1-60` · `internal/scheduler/scheduler_test.go:1-36`
**Problem:** `go test -race ./internal/ws/... ./internal/scheduler/...` pasa limpio, **pero solo porque los tests son secuenciales y triviales**: no hay ni un test que ejecute Broadcast y Unsubscribe en paralelo, ni Dispatch contra Unregister. Agregando ~20 líneas de test el race detector reporta panic inmediato en `hub.go:270` y `hub.go:233` (es exactamente así como se confirmaron los P0). El scheduler está en 12.9% y no tiene ningún test de `AddJob`/`RemoveJob`/`triggerJob`. La suite en verde da una falsa sensación de seguridad sobre la parte más frágil del sistema.
**Proposal:** Tests concurrentes de suscripción/broadcast y dispatch/unregister corriendo con `-race` en CI, y tests de `AddJob` idempotente, `RemoveJob` y solapamiento en el scheduler. Estos tests deben escribirse **antes** del fix de los P0 de concurrencia, para que fallen primero.
**Effort:** M · **Risk:** low · **Verificado:** CONFIRMED

---

## 5. Findings P2 y P3, agrupados por tema

Leyenda: **V** = ✓ verificado con evidencia citada · — = **sin verificar** (reportado por lectura de código, no confirmado)

### T1. Seguridad — secretos y credenciales

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | Connection strings de DB guardados y devueltos en claro por la API | `internal/models/models.go:59,100-102`, `internal/api/jobs.go:33-36`, `docs/schema.md:69` | Cifrar `source` (o el `conn_string`) con `crypto.Encrypt`; excluirlo del SELECT de list | M | ✓ |
| P2 | Connection string de DB en argv (`ps aux`) y propagado a `error_message` visible en la UI | `internal/backup/db.go:44,61,96,54`, `cmd/agent/main.go:262-265` | Credenciales por env (`PGPASSWORD`, defaults-file 0600, `--config` para mongo); sanitizar stderr antes de `sendFailure` | M | ✓ |
| P2 | SMTP password round-trip al browser y re-submit | `internal/api/settings.go:13-19`, `web/src/pages/Settings.tsx:46,185-186` | Redactar server-side; mandar solo si el usuario tipea | M | ✓ |
| P2 | Self-update ejecuta binarios descargados sin firma ni checksum | `cmd/agent/update.go:13,27-73` | `SHA256SUMS` firmado (minisign/cosign), clave pública embebida, verificar antes del rename; `http.Client{Timeout}` | M | ✓ |
| P2 | Secretos débiles aceptados y `.env.example` con clave de cifrado de 64 ceros | `internal/config/config.go:24-30`, `.env.example:6,9,12` | Rechazar placeholders y la clave nula; exigir `JWT_SECRET >= 32` bytes; dejar el `.env.example` vacío con el comando de generación | S | ✓ |
| P2 | `ENCRYPTION_KEY` nunca validada al arrancar: falla tarde y por request | `internal/config/config.go:39-47`, `internal/crypto/crypto.go:14-18` | `crypto.ValidateKey(hexKey)` llamada desde `config.Load` | S | ✓ |
| P2 | `crypto.Encrypt/Decrypt` sin associated data: ciphertexts intercambiables entre filas | `internal/crypto/crypto.go:31` | AAD con contexto (`"jobs.encrypt_passphrase:"+id`) en Encrypt y Decrypt | M | — |
| P2 | JWT de 24 h sin revocación: el logout no invalida nada | `internal/api/auth.go:39-49`, `internal/api/middleware.go:26-42` | `exp` a 1 h + refresh; claim `pwd_ver` validado contra `admin_users` | M | — |
| P2 | JWT en `localStorage` (accesible a cualquier XSS) | `web/src/lib/auth-context.tsx:14-20` | Ticket WS de un solo uso; a futuro, cookie `httpOnly` `SameSite` | L | — |
| P2 | El unit de systemd generado corre como root, sin hardening, con `ExecStart` al cwd | `cmd/agent/main.go:410-431` | Copiar el binario a `/usr/local/bin`; `NoNewPrivileges`, `ProtectSystem=strict`, usuario dedicado | M | — |
| P2 | `docker-compose` publica Postgres en `0.0.0.0` con password estático | `docker-compose.yml:8-9` | `"127.0.0.1:5432:5432"` | S | — |
| P3 | La API key se pasa como argumento de línea de comandos en ambos instaladores | `scripts/install.sh:8`, `scripts/install-agent.ps1:10-12` | Leer de stdin o de `NAT_BACKUP_API_KEY` | S | — |
| P3 | Oráculo de timing por usuario inexistente en el login | `internal/api/auth.go:25-37` | Comparar contra un hash bcrypt dummy precomputado cuando no hay usuario | S | — |
| P3 | El release publica binarios sin checksum ni firma; los 3 instaladores no verifican nada | `.github/workflows/release.yml:60-70`, `scripts/install.sh:69`, `install-agent.ps1:56`, `cmd/agent/update.go:27` | Generar `SHA256SUMS`, adjuntarlo, verificar con `sha256sum -c`/`Get-FileHash` antes de mover | M | ✓ |

### T2. Seguridad — hardening HTTP / WS / TLS

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | `CheckOrigin` siempre `true` en ambos upgraders WebSocket | `internal/api/runs.go:108-112`, `internal/api/console.go:115` | Validar Origin contra el host de la request o una whitelist por env | S | — |
| P2 | Sin security headers en la SPA ni en la API | `internal/api/server.go:79-111` | Middleware: `nosniff`, `Referrer-Policy`, CSP con `frame-ancestors 'none'`, HSTS si `TLSEnabled` | S | — |
| P2 | S3 sin server-side encryption solicitada | `internal/storage/s3.go:49-54`, `internal/models/models.go:109-115` | Campos `sse`/`kms_key_id`; setear `ServerSideEncryption` en PutObject | S | — |
| P2 | Backups locales escritos world-readable (0644) | `internal/storage/local.go:25` | `os.OpenFile(..., 0600)`; `sftpClient.Chmod(remotePath, 0600)` en SFTP | S | ✓ |
| P3 | `http.Server` TLS sin `MinVersion` explícita | `cmd/server/main.go:98-111` | `TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}` (hoy mitigado por el default de Go 1.25) | S | ✓ |
| P3 | Errores internos crudos filtrados al cliente (upload, browse) | `internal/api/upload.go:49`, `internal/api/agents.go:103` | Loguear y responder genérico; mapear `not connected`→409, timeout→504, buffer lleno→503 | S | — |
| P3 | Path traversal vía nombre de job en el filename de destino | `cmd/agent/main.go:299`, `internal/storage/local.go:24`, `sftp.go:67`, `s3.go:47` | Rechazar en los backends `filename != filepath.Base(filename)`; slugificar el nombre en la API | S | ✓ |
| P3 | Action de terceros pineada por tag mutable con `contents:write` | `.github/workflows/release.yml:8,61` | Pinear a SHA completo; mover `permissions` al job con el mínimo | S | — |

### T3. API HTTP — validación, contratos y respuestas

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | `handleUpdateJob` no valida los campos obligatorios que sí valida el create | `internal/api/jobs.go:92-121` | Extraer `(r createJobRequest) validate() error` y usarla en ambos handlers | S | — |
| P2 | `NewServer` no conecta el `ConsoleWriter` del agent handler; `NewServerWithStatic` sí | `internal/api/server.go:42-77` | Constructor común `newServer(db, cfg)`; que difieran solo en el router | S | — |
| P2 | Borrar un storage destination en uso devuelve 500 opaco en vez de 409 | `internal/api/storage.go:93-111` | Contar jobs que lo referencian → 409 `"in use by N job(s)"`; o traducir pq 23503 | S | — |
| P2 | `handleGetNotificationSettings` enmascara errores de DB como "no configurado" | `internal/api/settings.go:12-20` | Distinguir `sql.ErrNoRows` (200) del resto (log + 500); unificar el shape de la respuesta | S | — |
| P2 | `GET /api/runs` sin paginación, `LIMIT 100` fijo, polling cada 5 s | `internal/api/runs.go:57-84`, `web/src/pages/Runs.tsx:89` | `limit`+`cursor/offset` validados, devolver total, filtros por fecha y agente; paginar en la UI | M | ✓ |
| P3 | `SELECT *` en queries mapeadas a structs concretos | `internal/api/jobs.go:134,196,210`, `runs.go:25,93` | Enumerar columnas como en `handleListJobs` | S | — |
| P3 | Orden de middlewares: Logger y Recoverer antes que RequestID | `internal/api/server.go:115-117` | Reordenar a `RequestID, RealIP, Logger, Recoverer` | S | — |
| P3 | Lógica de validación de JWT duplicada entre middleware y console | `internal/api/console.go:104-112`, `middleware.go:26-35` | Extraer `parseJWT(tokenStr) (jwt.MapClaims, error)` único | S | — |
| P3 | Descriptor no cerrado en el fallback SPA; errores de escritura ignorados | `internal/api/server.go:101,171-175`, `upload.go:72` | Cerrar el file del chequeo; cerrar y verificar error antes del UPDATE en upload | S | — |
| P3 | `agentAuthMiddleware` responde 401 `"no db"` ante fallo de infraestructura | `internal/api/middleware.go:55-58` | 503/500; mejor, sacar el nil-check e inyectar sqlmock en los tests | S | — |

### T4. Concurrencia, WebSockets y scheduler

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | `Register` no desaloja la conexión previa del mismo agente | `internal/ws/hub.go:64-68,89-96` | Cerrar conexión y canal de la entrada existente antes de reemplazarla | S | — |
| P2 | Cron sin timezone explícita: DST puede saltear o duplicar backups | `internal/scheduler/scheduler.go:31` | `cron.WithLocation(loc)` desde env (default UTC); mostrar la zona en la UI y en `.env.example` | S | — |
| P2 | Un `SELECT name FROM agents` por cada línea de log de agente | `internal/ws/handler.go:115-127` | Cachear el nombre en el `AgentConn` durante el `hello` | S | — |
| P2 | `handleRunLogsWS` sin read pump ni cierre al terminar el run | `internal/api/runs.go:114-158` | Goroutine de lectura con `SetReadDeadline`/`PongHandler` y canal `done`; cerrar al llegar la línea terminal | S | — |
| P2 | Mensajes de agente desconocidos se descartan en silencio | `internal/ws/handler.go:110-168` | `default:` con log; validar `RunID` como UUID y `Percent` en 0..100 | S | — |
| P3 | `Scheduler.Stop` ignora el contexto de drenaje del cron | `internal/scheduler/scheduler.go:47-49`, `cmd/server/main.go:94` | `<-s.cron.Stop().Done()` con timeout; llamar `Stop()` antes de `httpSrv.Shutdown` | S | — |
| P3 | Cuatro copias del patrón lookup-agente + envío no bloqueante | `internal/ws/hub.go:144-260` | Extraer `sendTo(agentID, msg) error` — prerequisito para que el fix del P0 no quede a medias | S | — |

### T5. Backup — integridad y fidelidad del archivo

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | Paths fuente con el mismo basename se pisan dentro del archivo | `internal/backup/compress.go:54-58` | Prefijo namespaced por root (índice o path escapado); o fallar en validación del job ante basenames duplicados | M | ✓ |
| P2 | `OnProgress` solo se dispara una vez, al 100% | `internal/backup/files.go:78-80` | Pasar el callback a `TarCompress` e invocarlo por archivo (throttled) | M | — |
| P2 | El tamaño reportado son bytes sin comprimir, no el artefacto almacenado | `internal/backup/files.go:83`, `cmd/agent/main.go:327` | Contar bytes escritos a `w` con un counting writer; exponer el total crudo aparte si se quiere | S | — |
| P2 | El árbol fuente se recorre dos veces por backup | `internal/backup/files.go:32-45` | Que `TarCompress` cuente al recorrer y devuelva el count; borrar el walk previo | S | — |
| P2 | Sin patrones de exclusión para backups de archivos | `internal/backup/files.go:11-17` | `Exclude []string` en `FilesConfig`, matcheado en el WalkDir (`fs.SkipDir` para dirs), expuesto en el editor de jobs | M | — |
| P2 | `StderrPipe` con error descartado y luego `io.ReadAll` sobre un reader posiblemente nil | `internal/backup/db.go:46,63,98` | `cmd.Stderr = &bytes.Buffer{}` (sin lifecycle de pipe, sin caso nil) | S | — |
| P2 | Los comandos de dump no tienen contexto ni timeout | `internal/backup/db.go:28` | `exec.CommandContext` con `cmd.Cancel`/`WaitDelay`; duración máxima de job configurable | M | — |
| P3 | Los directorios pierden la barra final en el nombre del header de tar | `internal/backup/compress.go:58` | Re-agregar `/` cuando `d.IsDir()` | S | — |
| P3 | `DumpResult.SizeBytes` siempre cero salvo sqlite, y los callers lo descartan | `internal/backup/db.go:56,92`, `cmd/agent/main.go:263` | Counting writer en `Dump`, o eliminar `DumpResult` y devolver solo error | S | — |
| P3 | Fuga de goroutine y de pipe cuando `EncryptStream` falla a mitad | `internal/backup/files.go:65-67` | `pr.CloseWithError(err)` y drenar `errCh`; mínimo, `defer pr.Close()` (hoy no alcanzable: el writer es un `bytes.Buffer`) | S | ✓ |

### T6. Storage backends

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | Errores de `Close()` descartados en local y SFTP: backup truncado reportado como éxito | `internal/storage/local.go:29`, `sftp.go:77` | Close explícito con chequeo de error tras `io.Copy`; `f.Sync()` antes del Close en local | S | ✓ |
| P2 | Bytes subidos nunca verificados contra el tamaño esperado | `internal/storage/local.go:19` (y los tres backends) | `n, err := io.Copy(...)`; error si `size > 0 && n != size`; en S3 verificar ETag/checksum | S | — |
| P2 | S3 usa un único `PutObject`: techo de 5 GiB, sin multipart, sin retry, sin deadline | `internal/storage/s3.go:49-54` | `s3manager.Uploader` (multipart, retries por parte, acepta `io.Reader`) + `context.WithTimeout` | M | ✓ |
| P2 | S3 fuerza credenciales estáticas: bloquea roles IAM y la cadena por defecto | `internal/storage/s3.go:25-27` | Solo agregar `WithCredentialsProvider` si `AccessKey` y `SecretKey` no están vacíos; campo de session token | S | — |
| P2 | SFTP: escrituras secuenciales de 32 KB sin pipelining ni timeout de transferencia | `internal/storage/sftp.go:79,43` | `sftp.UseConcurrentWrites(true)`, `sftp.MaxPacket(1<<17)`, deadline en el copy, verificar tamaño final con Stat | M | — |
| P2 | Fallo de `MkdirAll` en SFTP logueado y la ejecución continúa | `internal/storage/sftp.go:68-71` | Stat del directorio tras el error; solo ignorarlo si está confirmado presente | S | — |
| P2 | `NewBackend` recibe `runID`/`serverURL`/`apiKey` y no los usa: `/api/upload` quedó sin callers | `internal/storage/interface.go:22`, `internal/api/server.go:134` | Decidir: implementar un backend `server` que los use, o borrar parámetros y ruta; actualizar `CLAUDE.md` y `README.md` | S/M | ✓ |
| P3 | El upload local no es atómico: un crash deja un parcial con nombre de backup válido | `internal/storage/local.go:24-33` | `destPath+".part"` → Sync → Close (con chequeo) → `os.Rename`; `defer os.Remove(tmp)` | S | ✓ |
| P3 | Prefijo de key S3 hardcodeado a `"nat-backup"` | `internal/storage/s3.go:47` | Campo `prefix` en `S3StorageConfig` (default `nat-backup`), opcional `<job>/<date>/` | S | — |
| P3 | Número mágico `30e9` en vez de una duración tipada | `internal/storage/sftp.go:43` | `30 * time.Second` como constante nombrada | S | — |

### T7. Criptografía y formato de archivo

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | El archivo cifrado no tiene header, versión ni parámetros de KDF | `internal/backup/encrypt.go:28-34,15-19` | Prepender magic + versión + id de KDF + iteraciones + chunk size; leer los parámetros de ahí en `DecryptStream` | S | — |
| P3 | El stream cifrado no protege contra truncado ni reordenamiento (chunks sellados independientes) | `internal/backup/encrypt.go:61-79` | AAD con contador de chunk big-endian + flag de último chunk; exigir chunk final en `DecryptStream`. *Nota: el splice entre backups queda refutado — hay salt aleatorio por stream; el truncado hoy lo atrapa zstd/tar aguas abajo* | M | ✓ |
| P3 | `DecryptStream` aloca una longitud controlada por el atacante (hasta 4 GiB por chunk) | `internal/backup/encrypt.go:111-112` | Rechazar `chunkLen` fuera de `[nonce+overhead, chunkSize+nonce+overhead]` antes de alocar; reusar buffer. *Hoy no alcanzable: sin callers no-test* | S | ✓ |

### T8. Agente — ciclo de vida, instalación y actualización

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | El agente ignora SIGTERM/SIGINT mientras está conectado: `systemctl stop` cuelga hasta el SIGKILL | `cmd/agent/main.go:57-72,153-157` | Contexto derivado del canal de señales; cerrar la conexión WS desde una goroutine al cancelarse para que `ReadMessage` retorne | S | ✓ |
| P2 | El self-update no funciona en Windows aunque la UI ofrece el botón | `cmd/agent/update.go:61,70` | `update_unix.go`/`update_windows.go` con build tags; en Windows renombrar a `.old`, reemplazar y relanzar con `os.StartProcess` | M | — |
| P3 | Sin rollback ni validación de integridad: una descarga truncada deja al agente muerto | `cmd/agent/update.go:52-64` | Backup del binario a `.bak`, validar tamaño contra `ContentLength`, smoke test `<nuevo> version`, restaurar ante fallo | M | ✓ |
| P3 | Reconexión con backoff fijo de 10 s sin jitter (thundering herd tras un restart) | `cmd/agent/main.go:60-72` | Backoff exponencial 1s→60s con jitter, reseteado al conectar | S | — |
| P3 | `agent install` deja un servicio que no arranca: el config referenciado no existe | `cmd/agent/main.go:416,424-430` | Aceptar `--server-url`/`--api-key` en el subcomando y generar `/etc/nat-backup/agent.yaml` con 0600; fallar claro si falta | S | — |
| P3 | El fallback a storage `local` cuando `storageType` viene vacío siempre falla | `cmd/agent/main.go:304-316`, `internal/storage/interface.go:29-31` | Fallar explícito con `"server did not provide storage configuration"` | S | — |

### T9. Retención, notificaciones y datos del servidor

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | El arranque del server resetea agentes pero deja runs colgados en `running` | `cmd/server/main.go:59-62` | Agregar el `UPDATE backup_runs SET status='failed', ... WHERE status='running'` (ver P0 de runs colgados) | S | ✓ |
| P2 | La retención nunca limpia runs `cancelled` ni `running` huérfanos | `internal/notify/retention.go:31`, `internal/api/runs.go:36` | Filtrar `br.status <> 'running'` (incluye `cancelled`) + regla aparte para `running` viejos | S | — |
| P2 | El scheduler de retención no corre al arrancar, no se puede detener y pierde ejecuciones | `internal/notify/retention.go:61-70` | Usar el `robfig/cron` ya presente (`@daily`) o `time.Timer` + `context` con `Stop()`; ejecutar una vez al iniciar | S | — |
| P2 | Fuga de conexión TLS si `smtp.NewClient` falla, y sin timeout de dial | `internal/notify/email.go:59-67` | `tls.DialWithDialer` con timeout + `defer conn.Close()`; `smtp.Dial` con dialer en la ruta no-TLS | S | — |
| P2 | Error del INSERT de admin descartado; `ADMIN_PASSWORD` ignorada tras el primer arranque | `cmd/server/main.go:54-57` | Capturar el error; `DO UPDATE SET password_hash = EXCLUDED.password_hash` o loguear explícitamente que se conserva | S | — |
| P2 | Estado `dirty` de golang-migrate deja el servidor sin arrancar y sin herramienta de recuperación | `internal/db/migrate.go:36-38`, `cmd/migrate/main.go:17` | Loguear versión y flag dirty antes del `Up()`; agregar subcomandos `version`, `force <v>`, `down 1` | M | — |
| P2 | Pool de DB sin `SetConnMaxLifetime` ni `SetConnMaxIdleTime` | `internal/db/db.go:17-18` | `SetConnMaxLifetime(30m)` + `SetConnMaxIdleTime(5m)`, configurables por env | S | — |
| P2 | `StorageDestination.Config` es `json.RawMessage` pero la columna guarda ciphertext base64 | `internal/models/models.go:49,85` | Cambiar ambos campos a `string` con `json:"-"` y exponer un DTO con el config descifrado y redactado | S | — |
| P3 | `retention_days` negativo borra TODOS los runs del job en la próxima corrida | `internal/notify/retention.go:30`, `internal/api/jobs.go:56-58`, `migrations/001:32` | `CHECK (retention_days > 0)` + validar en la API + `GREATEST(...,1)` y `COALESCE(finished_at, started_at)` en la query. *Mitigado hoy por `min={1}` en la UI, no por el backend* | S | ✓ |
| P3 | `RetentionCleaner.sender` nunca se usa | `internal/notify/retention.go:13,17` | Eliminar el campo, o implementar el resumen de cleanup que evidentemente se pretendía | S | — |
| P3 | Los mails de alerta no tienen `Date`, `Message-ID` ni `Content-Type` | `internal/notify/email.go:39-45` | Agregar los headers + `MIME-Version` y encodear el subject con `mime.QEncoding` | S | — |

### T10. UI web — bugs funcionales

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | El drawer de logs mantiene un snapshot viejo y nunca ve terminar el run | `web/src/pages/Runs.tsx:72,188` | Guardar solo `selectedRunId` y derivar el run del cache de `['runs']` | S | ✓ |
| P2 | El botón Copy tira una rejection sin manejar fuera de contextos seguros (http) | `web/src/pages/Agents.tsx:22-26` | Guard sobre `navigator.clipboard?.writeText`, `.catch` con fallback a `execCommand('copy')` y toast de error | S | ✓ |
| P2 | El contador "N buffered" del Console lee un ref en render y nunca se actualiza | `web/src/pages/Console.tsx:130-132,25` | Contador en state; mover `pausedRef.current = paused` a un efecto o usar updater funcional | S | ✓ |
| P2 | Los contadores "all time" del Dashboard solo cubren los últimos 100 runs | `web/src/pages/Dashboard.tsx:41-42,62-63`, `internal/api/runs.go:75` | Endpoint `GET /api/stats` con conteos server-side, o relabelar "last 100 runs" | M | ✓ |
| P2 | El gráfico de 7 días agrupa por fecha UTC y rotula en hora local | `web/src/pages/Dashboard.tsx:47-50` | Construir la key con componentes locales para que key y label coincidan | S | ✓ |
| P2 | El filtro de estado de runs se aplica en cliente sobre una página de 100 filas | `web/src/pages/Runs.tsx:88,103` | Poner `filterStatus` en la query key y pasarlo a `api.listRuns({status})` | S | ✓ |
| P2 | El WS del Console reconecta cada 3 s para siempre, sin backoff ni manejo de auth fallida | `web/src/pages/Console.tsx:53-62` | Backoff exponencial con techo y tope de intentos; detectar close code de auth y rutear a `/login`; guardar el timeout en un ref | M | ✓ |
| P2 | El WS de logs de run no reconecta y se traga los fallos | `web/src/pages/Runs.tsx:51-60` | Retry acotado con backoff mientras `status === 'running'`; estado explícito "connection lost — retrying" | M | ✓ |
| P2 | JWT expirado no detectado en cliente: el shell renderiza sobre un token muerto | `web/src/App.tsx:14-18` | Decodificar el claim `exp` al iniciar y tratarlo como deslogueado; limpiar el estado del auth context ante 401 en vez de recargar | S | — |
| P2 | Timeouts sin limpiar tras el unmount en Settings y Agents | `web/src/pages/Settings.tsx:336,341`, `Agents.tsx:127-130` | Guardar ids en refs y limpiarlos en el cleanup del efecto | S | — |
| P2 | `setState` sincrónico dentro de efectos (2 errores de ESLint) | `web/src/pages/Settings.tsx:46`, `Runs.tsx:21` | Derivar el form del query (`select` + `key`); devolver el estado vacío desde `useLiveLogs` | M | ✓ |
| P2 | La página Agents llama a `api.github.com` directamente desde el browser | `web/src/pages/Agents.tsx:77` | Proxear la consulta de release por el server (con cache) o hacerla opt-in; mostrar estado cuando falla | M | — |
| P2 | El diálogo de borrar agente miente y oculta el borrado en cascada de jobs y runs | `web/src/pages/Agents.tsx:386`, `migrations/001:25,42` | Corregir el texto y mostrar cuántos jobs y runs se van a eliminar | S | ✓ |
| P3 | El buffer del Console pausado crece sin límite | `web/src/pages/Console.tsx:46-47` | Cap igual que `entries` (`slice(-2000)`) con marcador "…truncated" | S | ✓ |
| P3 | El `ErrorBoundary` nunca se resetea: un error de render inutiliza todo el shell | `web/src/components/Layout.tsx:96-98`, `ErrorBoundary.tsx` | `key={useLocation().pathname}`; boundary de nivel superior en `main.tsx` | S | ✓ |
| P3 | El login envía el form con password vacía | `web/src/pages/Login.tsx` | `required` en ambos inputs + early return con mensaje local | S | — |
| P3 | El handler de 401 duplica la key del token como literal | `web/src/lib/api.ts:34`, `auth-context.tsx:3` | Exportar `TOKEN_KEY`; mejor, `UnauthorizedError` tipado manejado en el auth context | S | ✓ |
| P3 | El efecto de `useLiveLogs` lee `run` pero depende solo de `run?.id`/`run?.status` | `web/src/pages/Runs.tsx:63` | Depender del objeto completo tras el fix del drawer; memoizar el summary | S | ✓ |
| P3 | El Console hace smooth-scroll en cada línea entrante | `web/src/pages/Console.tsx:85-89` | `behavior: 'auto'` o throttle con rAF; `scrollTop = scrollHeight` directo; saltear si el usuario scrolleó arriba | S | — |

### T11. UI web — accesibilidad y convenciones

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | Strings en español en la página Agents (viola la convención de UI en inglés de `CLAUDE.md`) | `web/src/pages/Agents.tsx:36,149,240,243` | `Copied`/`Copy`, `Name is required`, `Download latest release and restart agent`, `Updating…` | S | — |
| P2 | 16 labels de formulario sin asociar a sus inputs | `web/src/pages/Jobs.tsx` (13 labels), `Storage.tsx:33,201,210` | `id` en cada control y `htmlFor` en cada label; derivar el id de la key en los helpers `field()` | M | — |
| P2 | Botones solo-ícono sin nombre accesible | `web/src/components/FileBrowser.tsx:66-71`, `Agents.tsx:246-253`, `Storage.tsx:181-186` | `aria-label` en cada uno | S | — |
| P2 | Sin manejo de `prefers-reduced-motion` | `web/src/index.css:167-206` | Envolver keyframes y transiciones en `@media (prefers-reduced-motion: no-preference)` o agregar el escape hatch | S | — |

### T12. UI web — tooling, type safety y dead code

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | `strict` apagado en `tsconfig.app.json` — y activarlo no cuesta nada | `web/tsconfig.app.json:2-24` | `"strict": true`. Verificado: `npx tsc -p tsconfig.app.json --strict --noEmit` da **0 errores** sobre el árbol actual | S | ✓ |
| P2 | La SPA no tiene tests ni test runner | `web/package.json:6-11` | Vitest + Testing Library; empezar por los payload builders de Storage/Jobs y los formatters | L | — |
| P2 | El alias `@/*` está configurado en tres lugares y no se usa nunca (98 imports `src/...`) | `web/vite.config.ts:8-12`, `tsconfig.app.json`, `components.json` | Elegir uno; migrar a `@/` alinea con `components.json` y con lo que genera `shadcn add` | M | — |
| P3 | El tipo `BackupRun.status` omite `cancelled`, que la API sí devuelve | `web/src/lib/types.ts:40`, `internal/api/runs.go:35` | Agregar `'cancelled'` al union y derivar `FILTERS` de `BackupRun['status']` | S | ✓ |
| P3 | Restos del template de Vite trackeados y nunca importados | `web/src/App.css`, `web/src/assets/*`, `web/public/icons.svg`, `web/README.md` | Borrar; reemplazar el README por una nota del workflow real | S | — |
| P3 | `src/lib/auth.ts` es un shim de re-export sin consumidores externos | `web/src/lib/auth.ts:1-2` | Borrarlo y apuntar los ~6 importers a `auth-context` | S | — |
| P3 | Transición CSS global sobre todos los elementos y pseudo-elementos | `web/src/index.css:135-142` | Acotar la transición a los elementos que cambian con el tema | S | — |
| P3 | `ConfirmDialog` hardcodea labels de Delete | `web/src/components/ConfirmDialog.tsx:55-58` | Props opcionales `confirmLabel`, `pendingLabel`, `variant` | S | — |
| P3 | 5 errores de `react-refresh/only-export-components` | `web/src/lib/auth-context.tsx:36,42`, `ui/badge.tsx:52`, `ui/button.tsx:58`, `ui/tabs.tsx:80` | Mover `useAuth`/`getToken` a `auth-hooks.ts`; override en eslint para los generados por shadcn | S | ✓ |
| P3 | `autoprefixer` es una devDependency sin uso | `web/package.json:38` | Quitarla (Tailwind v4 vía `@tailwindcss/vite` ya prefija) | S | — |

### T13. Ops, CI, build y documentación

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | No hay CI en push/PR y el workflow de release no corre tests, vet ni lint | `.github/workflows/release.yml` (único workflow) | `ci.yml` en push/PR con `go vet`, `gofmt -l`, `go test -race ./cmd/... ./internal/...`, `npm ci && npm run lint && npm run build`; el release depende de esos checks | M | ✓ |
| P2 | `install.sh` descarga un server linux/arm64 que el release nunca compila | `scripts/install.sh:44,79`, `release.yml:32-38` | Agregar el build de server linux/arm64, o restringir la instalación del server a amd64 con mensaje explícito | S | ✓ |
| P2 | `install.sh` no chequea el SO: en macOS instala binarios de Linux e informa éxito | `scripts/install.sh:42-46` | Validar `uname -s` y abortar fuera de Linux, o agregar builds darwin | S | — |
| P2 | El Makefile no tiene targets de lint, vet, race ni cobertura | `Makefile:1-36` | Targets `lint`, `test-race`, `cover`, llamados desde CI | S | — |
| P2 | `go ./...` entra en `web/node_modules` | `Makefile:27` | `web/go.mod` para sacar `web/` del patrón, o acotar a `./cmd/... ./internal/...` | S | — |
| P2 | El build de release usa `npm install` e ignora el lockfile commiteado | `Makefile:20`, `release.yml:30` | `npm ci` + cache de `actions/setup-node` | S | — |
| P2 | Nada verifica que `internal/api/static/dist` corresponda al `web/src` actual | `Makefile:8` | `build-server` depende de `build-ui`, o step de CI que reconstruya y falle si difiere | M | — |
| P2 | Gitlinks de submódulo rotos commiteados bajo `.claude/worktrees/` | `.claude/worktrees/agent-*` | `git rm --cached` ambas rutas + `.claude/worktrees/` al `.gitignore` | S | — |
| P2 | El comando del README para cargar el `.env` falla siempre | `README.md:70` | `set -a; . ./.env; set +a` | S | — |
| P2 | Instaladores y README enteramente en español (`CLAUDE.md` manda inglés para strings user-facing) | `scripts/install.sh`, `scripts/install-agent.ps1`, `README.md` | Traducir comentarios y salida de ambos scripts; decidir explícitamente si el README queda en español y registrar la excepción en `CLAUDE.md` | M | — |
| P2 | `docs/schema.md` desincronizado con la migración 002 (`notification_settings.config` sigue como JSONB) | `docs/schema.md:103,33` | Cambiar a TEXT y extender la nota de la migración 002 a ambas columnas | S | ✓ |
| P2 | `docs/schema.md` no documenta el estado `cancelled` de `backup_runs` | `docs/schema.md:106`, `internal/api/runs.go:36` | Documentarlo; agregar `models.RunStatusCancelled` y reemplazar los literales | S | — |
| P3 | La migración 002 down no es reverso válido: el rollback rompe sobre datos cifrados | `migrations/002_storage_config_text.down.sql:1-2` | Reconocerla como irreversible (`RAISE EXCEPTION`) en vez de dejar un down que falla a mitad | S | ✓ |
| P3 | `.PHONY` declara un target `migrate-up` que no existe | `Makefile:1` | Agregar la regla real o quitarlo; incluir `build-agent-windows` en `.PHONY` | S | — |
| P3 | `install.sh` ignora en silencio los argumentos desconocidos | `scripts/install.sh:20-30` | `*) err "Unknown option: $1" ;;` + validar que el operando exista | S | — |
| P3 | El bloque de config de `install.sh` es contradictorio y asume que `sudo` existe | `scripts/install.sh:85-95` | `SUDO=""` cuando EUID=0 o el destino es escribible; usar `"$SUDO"` en todas las escrituras | S | — |
| P3 | Comentario del `.gitignore` sobre `web/dist` es falso | `.gitignore:14` | Corregirlo para que apunte a `internal/api/static/dist` | S | — |
| P3 | Nombre partido del proyecto: `nat-backup` vs `negra-backup` | `go.mod:1`, `README.md:1`, `scripts/install.sh:13`, `cmd/agent/update.go:14` | Documentar el mapeo en el README o unificar | S | — |
| P3 | Sin índice en las FKs de `backup_jobs` | `migrations/001_initial_schema.up.sql:65-68` | Migración con `idx_backup_jobs_agent_id` e `idx_backup_jobs_storage_destination_id`; actualizar `docs/schema.md:131` | S | — |
| P3 | `internal/db/migrate.go` sin formatear (y 3 archivos más) | `internal/db/migrate.go:4-11`, `internal/api/server.go`, `internal/ws/hub.go`, `internal/ws/types.go` | `gofmt -w` + target `fmt-check` en CI | S | ✓ |

### T14. Testing

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | Cero cobertura del check IDOR de upload — la única autorización a nivel recurso del sistema | `internal/api/upload.go:34-47` | Tests de tabla con testcontainers/sqlmock: agente A→run de B = 403; inexistente = 404; no-`running` = 409; happy path = 200 con `size_bytes` actualizado | M | — |
| P2 | Sin tests de `agentAuthMiddleware`, `ConsoleHub` ni de ningún handler autenticado (13% de cobertura) | `internal/api/middleware.go:46-87`, `console.go:35-86` | Tests unitarios de `ConsoleHub` con `-race` (subscribe/unsubscribe concurrente + Write) y tests de handler con JWT firmado sobre sqlmock | M | — |
| P2 | El path de borrado de retention sin ningún test; `cmd/agent/main.go` en 0% | `internal/notify/retention_test.go:11-19` | Tests de retention contra Postgres con skip `-short`: retención cumplida/no cumplida, valores límite, estados no elegibles; tests puros de `toWSURL` | M | — |
| P2 | Sin tests de los backends S3 y SFTP ni de ningún camino de error (18.2% en storage) | `internal/storage/local_test.go:1-40` | Suite de contrato del `Backend` contra local + stub MinIO (httptest con el endpoint override que ya existe) + servidor SSH/SFTP in-process; assertear modo 0600, rechazo de traversal y propagación de errores en short writes | M | — |

---

## 6. Temas transversales

Estos son los patrones que aparecen en 3+ áreas. Arreglar el patrón, no solo la instancia.

### TT1. Fan-out de canales sin sincronización (3 sitios, 1 causa, 1 fix)
`ConsoleHub.Write`, `Hub.BroadcastRunLog` y los cuatro métodos de `Hub` que escriben a `AgentConn.send`. Todos copian bajo lock, sueltan y envían; todos tienen un desuscriptor que hace `close(ch)`. Dos de los tres **reproducidos con `-race`**, ambos con panic que mata el proceso. La existencia de cuatro copias del mismo patrón en `hub.go:144-260` es la razón por la que hay que extraer un `sendTo()` antes de arreglar: si no, el fix va a quedar aplicado en tres de cuatro.
*Findings:* P0 `send on closed channel`, P3 `Cuatro copias del patrón`, P1 `Cero cobertura de caminos concurrentes`.

### TT2. Errores tragados que degradan en silencio (viola directamente `CLAUDE.md` → "arreglá la causa, no el síntoma")
El patrón dominante del repo: cuando algo falla, se loguea y se sigue como si nada — y el resultado se reporta como éxito.
- `crypto.Decrypt` de la passphrase falla → se despacha sin cifrar y el archivo se llama `.enc` (P0).
- `crypto.Decrypt` del storage falla → se despacha con config vacía y el agente hace todo el trabajo para nada (P1).
- `DispatchJob` no puede entregar → run en `running` para siempre, HTTP 201 (P0).
- `sched.AddJob` falla por cron inválido → job creado, `enabled:true`, nunca corre (P1).
- `Close()` de local y SFTP descartado → archivo truncado reportado como éxito (P2).
- `wal_checkpoint` de sqlite descartado → backup inconsistente reportado como éxito (P1).
- `MkdirAll` de SFTP logueado y se sigue → error final apunta al archivo, no al directorio (P2).
- `StderrPipe()` descartado → posible `io.ReadAll(nil)` → panic (P2).
- `INSERT` de admin sin chequear error (P2). `json.Encode` de la respuesta sin chequear (P3).
- `handleGetNotificationSettings` convierte cualquier error de DB en "no configurado" (P2).

### TT3. Contratos entre capas sin tipar ni validar
Nada verifica que la UI y el server hablen el mismo idioma, y nada valida la entrada.
- `config: Record<string, unknown>` en el cliente contra structs Go tipados → **S3 y SFTP rotos de fábrica** (P0), y detectado solo por comparación manual de json tags.
- `handleUpdateJob` sin las validaciones de `handleCreateJob`: un PUT `{}` corrompe el job (P2).
- Expresión cron aceptada sin parsear (P1). `retention_days` negativo aceptado (P3, mitigado solo por el `min={1}` de HTML5). `RunID` de agente usado sin validar que sea UUID (P2).
- Sin `MaxBytesReader` en ningún endpoint; sin `SetReadLimit` en ningún WebSocket (P1 ×2).
- `strict: false` en el tsconfig de la SPA, y activarlo da **0 errores** — el type safety está apagado gratis (P2).

### TT4. Secretos en tránsito y en reposo, inconsistentes
El modelo de "cifrado en reposo" que documenta el README está aplicado a la mitad de las cosas.
- Cifrado: `storage_destinations.config`, `backup_jobs.encrypt_passphrase`. Sin cifrar: `notification_settings.config` (password SMTP, P0) y `backup_jobs.source` (connection strings de DB con credenciales, P2) — ambos **devueltos por la API al browser**.
- En tránsito: passphrase y credenciales de storage viajan **descifradas por `ws://`** con la config por defecto (P1); el JWT viaja en query string y termina en los logs (P0); el conn string de DB va en argv, visible en `ps` (P2), y su stderr se persiste en `error_message` visible en la UI (P2).
- En el historial de git: `ENCRYPTION_KEY`, `JWT_SECRET`, `ADMIN_PASSWORD` y el `DATABASE_URL` en un repo público (P0).
- Sin verificar: host key SFTP (P1), checksum/firma de los binarios de release y del self-update (P2/P3), ACL del `agent.yaml` en Windows (P1).

### TT5. Ausencia total de red de seguridad
No hay CI en push/PR (P2). Cobertura: 13% en `api`, 12.9% en `scheduler`, 18.2% en `storage`, 0% en `cmd/agent`. `gofmt -l` reporta 4 archivos y eslint 11 errores **sin que nada falle**. Los tests que existen pasan `-race` solo porque son secuenciales; agregando 20 líneas de test concurrente aparece el panic (P1). Las cuatro cosas de mayor riesgo del sistema — el check IDOR de upload, el borrado de retention, los hubs concurrentes y los backends S3/SFTP — no tienen **ningún** test. Los cuatro P0 de contrato roto (S3, SFTP, passphrase, config wipe) son exactamente la clase de regresión que un puñado de tests hubiera atajado antes de shippear.

### TT6. El estado `running` no tiene dueño
Cinco caminos lo crean o lo dejan huérfano y ninguno lo cierra: dispatch a agente offline, buffer lleno, fallo de storage post-insert, caída del WS a mitad de job, restart del servidor. No hay reaper, ni timeout, ni watchdog, ni reset al arrancar. Y como la retención filtra `status IN ('success','failed')`, esas filas se acumulan para siempre. Es un solo bug de diseño (el run se crea antes de saber si se puede ejecutar) manifestándose en cinco lugares.

---

## 7. Cosas que faltan hacer (roadmap funcional)

### 7.1 Features prometidas y NO implementadas

Ordenadas por impacto en el producto.

| # | Feature | Dónde se promete | Estado real |
|---|---|---|---|
| 1 | **Restaurar un backup** | Implícito en el producto; `docs/superpowers/specs/...design.md` | **Inexistente.** `Backend` solo tiene `Upload`; `DecryptStream` solo tiene callers de test; no hay ruta, ni comando, ni UI |
| 2 | **Borrado de archivos por retención** | `design.md:264-267`, `README.md:11` | **Stub.** Loguea `"would delete storage"` y borra la fila, perdiendo el `storage_path` |
| 3 | **Notificaciones por email ante fallas** | `README.md:12`, `design.md:273` | **No funciona.** El sender se congela al arrancar; configurarlo desde la UI no tiene efecto; en instalación nueva es `nil` para siempre |
| 4 | **Logs en tiempo real durante un backup** | `README.md:15`, `CLAUDE.md` | **Muerto.** El WS exige header `Authorization`, que el browser no puede mandar → 401 siempre |
| 5 | **"Completed runs replay stored logs from DB"** | `CLAUDE.md` | **Falso.** No existe tabla de logs; `handleRunLogs` devuelve solo status + error_message; el frontend fabrica 4-5 líneas cosméticas |
| 6 | **Streaming tar + upload chunked multipart** | `design.md:237-240`, plan 4 | **Ausente.** Todo en `bytes.Buffer` en RAM; S3 con un único `PutObject` |
| 7 | **Progreso en tiempo real (`job_progress`)** | `design.md:212`, `README.md:15` | **Falso.** `OnProgress` se invoca una vez, al final, con `100` |
| 8 | **Servidor como destino de storage (`POST /api/upload/{run_id}`)** | `README.md:144`, `CLAUDE.md` | **Código muerto.** El endpoint existe y está autenticado, pero `NewBackend` descarta los tres parámetros que lo habilitarían; ningún caller |
| 9 | **`install` registra un servicio de Windows** | `design.md:344-345`, `README.md:91` | **Solo imprime comandos** para copiar, e igual reporta "agent installed as system service" |
| 10 | **Self-update en Windows** | UI ofrece el botón; el asset `.exe` se genera | **Falla siempre.** `syscall.Exec` = `EWINDOWS`; `os.Rename` sobre un `.exe` en ejecución da sharing violation |
| 11 | **TLS con cert self-signed auto-generado** | `design.md:230` | **Solo bring-your-own.** Sin cert files y `TLS_ENABLED=true` → `log.Fatalf` |
| 12 | **Botón de email de prueba** | `design.md:273` | **No existe** ni el endpoint ni el botón |
| 13 | **Deployment containerizado del server** | `design.md:317-332` | **No hay Dockerfile**; el compose solo define `postgres` |
| 14 | **`private_key` para SFTP** | `design.md:258`, `README.md:142` | El modelo Go lo soporta; **la UI no ofrece el campo** |
| 15 | **Descarga del binario del agente desde el server** | `design.md:337-338` | Hardcodeado a GitHub Releases público; **falla en redes sin salida** |

### 7.2 Features a medias (existen pero incompletas)

| # | Feature | Qué falta |
|---|---|---|
| 1 | **Filtros y paginación de runs** | `LIMIT 100` fijo sin offset/cursor; la UI ni siquiera usa los filtros `status`/`job_id` que la API ya soporta; faltan filtros por agente y rango de fechas (`design.md:193,302-303`) |
| 2 | **Vista de agentes** | Solo lista. Falta el detalle `/agents/:id` con sus jobs e historial (`design.md:286-288`) |
| 3 | **Lista de jobs** | Falta "last run" (calculable con el cache actual) y "next run" (el scheduler tiene `cron.Entry.Next` y no lo expone) (`design.md:290`) |
| 4 | **Reintentos** | No hay política de retry ni columna `attempt`; tampoco botón Retry en la UI. Un fallo transitorio de red deja el backup del día sin hacer |
| 5 | **Verificación de integridad** | No se calcula ni almacena checksum del archivo subido; una subida truncada se reporta `success` y se descubre al restaurar |
| 6 | **Gestión del usuario admin** | `ON CONFLICT DO NOTHING` hace que cambiar `ADMIN_PASSWORD` no tenga efecto tras el primer boot, y no hay endpoint ni UI de cambio de contraseña. Si se filtra, la única salida es un UPDATE manual en Postgres |
| 7 | **Notificaciones** | Solo `SendFailureAlert`. Falta notificación opcional en éxito, flag `enabled` y cualquier throttling (un job que falla cada 5 min genera un mail por corrida) |
| 8 | **Exclusiones en backups de archivos** | No hay forma de saltear `node_modules`, `.git`, caches o logs enormes dentro de un directorio seleccionado |
| 9 | **`GET /api/runs/{id}/logs`** | Registrado, sin ningún consumidor. Debería ser el endpoint de logs históricos una vez que exista la tabla |
| 10 | **`cancelled` como estado** | Se escribe desde la API y se renderiza en la UI, pero no tiene constante en `models`, no está en el tipo TS, no está en `docs/schema.md` y la retención no lo purga |

---

## 8. Quick-wins

Effort S + Risk low + valor alto. Estos primero: ~1-2 días de trabajo total y sacan de encima 3 P0 y varios P1.

| Finding | Sev | Location | Por qué primero |
|---|:-:|---|---|
| Fix del fan-out de canales (no cerrar desde el desuscriptor) | P0 | `hub.go:262-300`, `hub.go:64-96`, `console.go:45-101` | Elimina 3 panics que matan el proceso; el fix es de pocas líneas y está reproducido con `-race` |
| Passphrase: no pisar la columna en el update | P0 | `jobs.go:104-118` + `Jobs.tsx:204,224` | Corta la pérdida de cifrado en el punto más barato de la cadena |
| `BackupFiles` falla con `Encrypt && Passphrase == ""` | P0 | `files.go:50` | Convierte un backup silenciosamente en claro en un error visible |
| Corregir los json keys de S3/SFTP en la UI | P0 | `Storage.tsx:55-56,63-66` | Desbloquea 2 de 3 backends con un rename de strings y un `parseInt` |
| WS de logs: mover a `?token=` | P0 | `server.go:162` + `Runs.tsx:49` | Revive una feature entera; ya existe el patrón en `handleConsoleWS` |
| Redactar `token` del log de la URI | P0 | middleware nuevo en `server.go:115` | Mitigación inmediata del JWT filtrado, sin tocar el esquema de auth |
| `UPDATE backup_runs ... WHERE status='running'` al arrancar | P0(parte) | `cmd/server/main.go:59-62` | Dos líneas; limpia el historial en cada restart mientras se hace el fix completo |
| `SetReadLimit` en los cuatro upgraders | P1 | `handler.go:57,96`, `console.go`, `runs.go:121` | Cuatro líneas; cierra un OOM pre-auth |
| `MaxBytesReader` global + en upload | P1 | `server.go:113`, `upload.go:74` | Un middleware; cierra dos DoS |
| Rate limit en `/api/auth/login` y `/ws/console` | P1 | `auth.go:18`, `console.go:122` | `httprate.LimitByIP`, una línea por ruta |
| Validar el cron con `cron.ParseStandard` | P1 | `jobs.go:49,132,194` | Evita jobs fantasma que el usuario cree activos |
| `RemoveJob` al borrar un agente | P1 | `agents.go:28-46` | Corta el spam infinito de violaciones de FK en el cron |
| Mover el `UPDATE ... offline` dentro del guard `current == ac` | P1 | `hub.go:70-83` | Una línea; la UI deja de mentir sobre agentes conectados |
| `select`+`default` en `DeliverBrowseResult` | P1 | `hub.go:214-221` | Una línea; evita que un agente quede mudo para siempre |
| ACL del `agent.yaml` en Windows | P1 | `install-agent.ps1:74-78` | Paridad con lo que ya hace el instalador de Linux |
| `"strict": true` en `tsconfig.app.json` | P2 | `web/tsconfig.app.json` | **Verificado: 0 errores.** Type safety gratis, cero migración |
| `gofmt -w` + `npx eslint --fix` donde aplique | P3 | 4 archivos Go, varios TSX | Deja el árbol limpio antes de meter el gate de CI |
| `docker-compose`: bindear a `127.0.0.1` | P2 | `docker-compose.yml:8-9` | Un cambio de string; saca la DB de dev de la LAN |

---

## 9. Programa por fases

### Fase 1 — Seguridad y pérdida de datos (bloqueante; nada nuevo hasta cerrar esto)

**Objetivo: dejar de perder datos y de exponer secretos.**

1. **Rotación de secretos + purga del historial.** `.env` en repo público (P0). Rotar `JWT_SECRET`, `ADMIN_PASSWORD`, password de Postgres y `ENCRYPTION_KEY` (con re-cifrado de datos existentes); `git filter-repo` que en la misma pasada saque el `.env` y los 6 binarios (P1); force-push.
2. **Cadena de cifrado de backups.** Passphrase borrada al editar (P0) + passphrase vacía → plaintext (P0) + `triggerJob` despacha con decrypt fallido (P1). Los tres se arreglan juntos o no se arregla ninguno.
3. **Los tres panics de `send on closed channel`** (P0), precedidos por el refactor de `sendTo()` (P3) y por los tests concurrentes que fallan primero (P1).
4. **Secretos en claro y en tránsito.** SMTP en DB y en la API (P0); JWT en query string (P0); conn strings de DB devueltos por la API (P2); passphrase por `ws://` sin TLS (P1); host key SFTP (P1); ACL de Windows (P1).
5. **Autorización.** Scopear los UPDATE de `job_done`/`job_failed`/`job_progress` por agente (P0).
6. **Retención.** `Delete` en `storage.Backend` + borrar el archivo antes que la fila (P1). Hasta que esté, considerar **desactivar el cleaner**: hoy destruye la única referencia al archivo sin borrarlo.
7. **DoS baratos.** `MaxBytesReader`, `SetReadLimit`, rate limit de login, límite de upload (P1 ×2).

### Fase 2 — Correctness

**Objetivo: que lo que dice que funciona, funcione.**

1. **Storage backends S3/SFTP** (P0): json keys, tipos, `private_key`, union discriminada en TS, validación de shape server-side. Sin esto, 2 de 3 backends no existen.
2. **Ciclo de vida de `running`** (P0): `DispatchJob` devuelve error, validar agente online antes del insert, marcar failed ante cualquier fallo posterior, reaper en `Unregister` y al arrancar, timeout de run.
3. **WS de logs alcanzable desde el browser** (P0) — después del ticket de auth de la Fase 1.
4. **El walk de tar** (P1): symlinks, sockets, FIFOs, skew de tamaño. Hoy cualquier árbol real tiene chances altas de romper el backup entero.
5. **Dumps de DB** (P1 ×2): mysqldump con flags correctos y guard `--`; sqlite con `.backup`/`VACUUM INTO` en vez de copia cruda.
6. **Streaming del agente** (P0): temp file como paso intermedio, `io.Pipe` + `s3manager.Uploader` como destino.
7. **Notificaciones**: releer la config al enviar (P1). **Config wipe** al editar un storage destination (P1).
8. **Solapamiento de runs** (P1), **cron huérfano al borrar agente** (P1), **validación de cron** (P1), **uploads fuera de `/tmp`** (P1), **`Hub.Run` sin I/O de DB** (P1), **`DeliverBrowseResult`** (P1), **unregister viejo** (P1).
9. **Validación al arrancar**: `ENCRYPTION_KEY` de 32 bytes, `JWT_SECRET >= 32`, rechazo de placeholders (P2 ×2).

### Fase 3 — Gaps funcionales

**Objetivo: que el producto sea un producto de backup.**

1. **Restore** (P1). Empezar por el comando del agente (desbloquea el caso de uso), después `Download`/`List` en `Backend` + endpoint + botón.
2. **Persistencia de logs de run** (P2): tabla `run_logs`, replay antes de suscribir, y `GET /api/runs/{id}/logs` pasa a servirlos. Corregir `CLAUDE.md` mientras tanto.
3. **Progreso real** (P2): callback dentro de `TarCompress`.
4. **Checksum de integridad** (P2): sha256 en streaming, columna nueva, verificación en restore.
5. **Paginación y filtros server-side de runs** (P2), **detalle de agente** (P2), **last/next run en jobs** (P3).
6. **Reintentos** (P2) y **cambio de password del admin** (P2).
7. **Decidir sobre `/api/upload`**: implementar el backend `server` o borrar ruta + parámetros muertos y corregir `README.md`/`CLAUDE.md` (P2).
8. **Instalación**: servicio real de Windows (P2), self-update en Windows (P2), config generado en `install` (P3), cert self-signed (P2), Dockerfile + compose (P2).
9. **Exclusiones** (P2), **email de prueba** (P2), **notificación en éxito + throttling** (P3).

### Fase 4 — Deuda y tooling

**Objetivo: que la Fase 1-3 no se vuelva a romper.**

1. **CI en push/PR** (P2): `go vet`, `gofmt -l`, `go test -race ./cmd/... ./internal/...`, `npm ci && lint && build`; el release depende de esos checks. Junto con `web/go.mod` o el acote de patrones para sacar `node_modules` (P2) y los targets del Makefile (P2).
2. **Tests de lo crítico** (P2 ×4): IDOR de upload, `ConsoleHub` con `-race`, retention, contrato de los backends. La SPA con Vitest (P2).
3. **`strict: true`** en el tsconfig (P2, verificado sin costo) y limpieza de los 11 errores de eslint.
4. **Firma y checksum del release** + verificación en los tres instaladores y en el self-update (P2/P3). Pinear actions a SHA (P2). `npm ci` en vez de `npm install` (P2).
5. **Hardening HTTP**: security headers (P2), `CheckOrigin` (P2), rotación/revocación del JWT (P2), `MinVersion` de TLS (P3).
6. **Docs**: `docs/schema.md` (002 y `cancelled`), README (comando de `.env`, idioma, mapeo `nat-backup`/`negra-backup`), `CLAUDE.md` (logs no persistidos). Instaladores a inglés.
7. **Higiene**: gitlinks de `.claude/worktrees/` (P2), restos del template de Vite (P3), `auth.ts` shim (P3), `SELECT *` (P3), orden de middlewares (P3), índices de FK (P3), `migrate` con `version`/`force`/`down` (P2), pool de DB con lifetime (P2), accesibilidad (P2 ×3), cron con timezone explícita (P2).

---

*Informe generado el 2026-08-04 sobre `4de649c`. Ningún archivo del proyecto fue modificado salvo este documento.*
