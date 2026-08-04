# Roadmap nat-backup

> Consolidado de 6 lentes de producto. Cada propuesta con impacto CRÍTICO o ALTO fue verificada
> contra el código actual (rama `main`, excluyendo `.claude/worktrees/`) antes de entrar acá.
> Fecha: 2026-08-04.

---

## 1. Resumen en una frase

El producto **hace backups muy bien y no sabe casi nada de lo que pasa después**: no avisa cuando
dejan de correr, no se puede restaurar desde la interfaz, y no se respalda a sí mismo — las tres
cosas que separan "tengo backups" de "puedo recuperarme".

---

## 2. Los 5 que importan

### 1. Alertar cuando el backup NO corre (dead-man's switch) + notificar desde el scheduler
**Esfuerzo: M** (la primera mitad es S y ya tapa el 70%)

El único disparador de mail en todo el sistema es `internal/ws/handler.go:182`, alcanzable
únicamente cuando un agente **conectado** manda `job_failed`. El struct `Scheduler` no tiene ni
siquiera un campo de notificación (verificado: `grep -n "notif" internal/scheduler/scheduler.go`
→ 0 resultados). Entonces: agente apagado (`DispatchJob` → "agent X is not connected"), destino
irresoluble, passphrase indescifrable, run solapado, run zombie tras reinicio del server — todos
escriben una fila `failed` en la base y **nadie recibe nada**.

El caso peor no deja ni fila: si el agente nunca se conecta, o alguien deshabilita un job con el
toggle, el silencio es idéntico al éxito. Un servidor que se apaga un viernes genera tres noches
sin backup y cero mails.

Tres piezas: (a) pasar el `DBNotifier` al Scheduler y llamarlo en `recordFailedRun` / `failRun` /
`recordSkippedRun`; (b) chequeo periódico de freshness — job habilitado sin run exitoso en N
intervalos de su cron, con `Schedule.Next()` que ya se usa en `ValidateCron`; (c) alerta de agente
offline por `agents.last_seen`. **Anti-spam obligatorio**: una alerta por incidente agrupada por
agente, no una por tick — un agente caído con 15 jobs manda 15 mails por noche y el operador filtra
la carpeta a la basura, que es exactamente el fallo que se quería evitar. Sumar un canal webhook
(Telegram/ntfy/Slack, ~30 líneas) además de SMTP: el formulario de SMTP arranca vacío y es opcional,
así que en la instalación típica **no hay ningún canal configurado**.

### 2. Restore disparado desde la UI, completo y selectivo
**Esfuerzo: L**

`RestorePanel` (`web/src/pages/Runs.tsx:202-274`) no restaura: imprime un comando para copiar y
pegar, con un comentario explícito que dice *"There is no restore endpoint on the server"*.
Verificado: el router (`internal/api/server.go:165-224`) no tiene ninguna ruta de restore ni de
download, y `internal/ws/types.go:11-21` no define ningún `MsgTypeRestore` — el servidor **no tiene
forma de pedirle un restore al agente**.

Para recuperar algo hay que: conseguir SSH al host del agente, tener el binario ahí, armar a mano un
`destination.json` con credenciales de S3/SFTP que el servidor tiene cifradas y deliberadamente no
manda al navegador, exportar `NAT_BACKUP_PASSPHRASE` y correr el CLI. Y aun así es todo-o-nada:
`RestoreOptions` sólo expone `DryRun`, `OnEntry` y `Logf`, sin filtro de paths — recuperar un
archivo de 4 KB obliga a bajar y extraer los 500 GB.

Se apoya en piezas que ya existen: `Backend.Download()` está en los tres backends, el canal WS ya
transporta `storage_config` descifrado y passphrase para `run_job`, y el streaming de progreso ya
está resuelto. Mínimo viable en dos capas: `MsgTypeRestore` con `paths[]` + destino reusando el
mismo canal y el mismo stream de logs, más filtro de entradas en `RestoreWithOptions`.
**Riesgo a acotar**: un restore con destino arbitrario es escritura remota de archivos con los
permisos del agente — directorio de staging por agente o allowlist, y auditar quién lo pidió.

### 3. DR del propio servidor: self-backup + kit de recuperación de passphrases
**Esfuerzo: M**

El producto respalda todo menos a sí mismo. Verificado: `grep -rn pg_dump --include=*.go` pega
únicamente en `internal/backup/db.go:44` (el dumper de bases del usuario). No hay tabla, job de
sistema, endpoint ni comando que respalde la DB del servidor. `ls cmd/` = server, agent, migrate,
rotate-key.

`backup_jobs.encrypt_passphrase` y `storage_destinations.config` viven cifrados con
`ENCRYPTION_KEY`, que vive en el `.env` del servidor. Si se muere ese disco: los `.enc` siguen sanos
en S3 y son **ruido irreversible**. No es "perdí la herramienta", es que todos los backups
históricos de todos los servidores se convirtieron en basura. Es el único punto de falla del diseño
que destruye retroactivamente años de trabajo.

Dos entregables y ambos son necesarios: (1) job de sistema que dumpee el propio Postgres a un
destino configurable con su retención — reusa toda la maquinaria existente; (2)
`nat-backup-server export-recovery-kit`, un archivo cifrado con una passphrase que pide al operador
(**nunca** con `ENCRYPTION_KEY`, que se pierde junto con el servidor) con el mapeo
job → passphrase → destino → último `storage_path`, para imprimir o guardar en un gestor de
contraseñas. Sin (2), (1) es circular: el dump también está cifrado con la llave perdida.

### 4. Salud por job y por agente en la lista (último éxito, antigüedad, próxima ejecución)
**Esfuerzo: S**

La mejor relación valor/esfuerzo de toda la lista, y **comparte exactamente la consulta de la
propuesta 1** — hacerlas en el mismo sprint duplica el valor por el mismo backend.

Verificado: `handleListJobs` (`internal/api/jobs.go:66-78`) es un `SELECT` plano sobre
`backup_jobs`, sin tocar `backup_runs`. `grep -rn "last_run|last_success|next_run"` sobre
`internal/`, `cmd/`, `web/src/` y `migrations/` → **cero resultados en todo el repo**. La página que
el admin mira para responder "¿está todo bien?" muestra tipo, agente, destino y cron humanizado, y
nada más. Los tiles del Dashboard son acumulados de toda la historia, así que 3 fallas de hace seis
meses inflan para siempre un número que nadie puede interpretar.

Un `LEFT JOIN LATERAL` sobre `backup_runs` por `job_id` (el índice `idx_backup_runs_job_id` ya está)
y `cron.Parse(...).Next(time.Now())` con el parser que ya se usa. Lo valioso no es una tabla más
sino el orden: primero lo atrasado, agrupado por agente, con semáforo relativo al schedule (un job
diario en rojo a las 26 h, uno semanal a los 8 días). **Trampa de UX**: "último run" incluye las
filas sintéticas `attempt=0` de `recordSkippedRun`, que son `failed` pero significan "se solapó", no
"se rompió" — distinguirlas o el usuario ve rojo donde no lo hay.

### 5. Exclusiones por job + tolerancia a archivos ilegibles
**Esfuerzo: S + S**

Las dos features más baratas del motor de backup y las dos que hoy hacen que el producto sea
inviable en las máquinas reales de una PyME.

**Exclusiones**: verificado, `grep -rniE "exclude" internal/ cmd/ web/src/` da un solo hit y es un
comentario en `storage.go` sobre otra cosa. `FilesSource` es `{Paths []string}` y el `WalkDir` de
`compress.go:54-117` tara toda entrada que encuentra. Apuntar a `/var/www` — que es lo que hace todo
el mundo — mete `node_modules`, `.git`, `vendor/`, `.venv` y caches: 2 GB de código útil se
convierten en 12 GB de tar cada noche, y son bytes reconstruibles con un `npm install`. `source` es
JSONB, así que no hace falta migración; el trabajo es el matcher (devolver `fs.SkipDir` al matchear
un directorio, clave para no ni siquiera recorrer `node_modules`) y el editor de patrones. Aplicarlo
**también** al walk de conteo de `files.go:52-63` o el porcentaje de progreso queda mal.

**Tolerancia a ilegibles**: verificado, `compress.go:55-57` hace `if werr != nil { return werr }` —
un solo archivo que no se puede abrir aborta el run entero. Un temporal que se borró a mitad del
recorrido (pasa todo el tiempo en `/var` y `/home`), un archivo de root en un agente no-root, un
socket, matan una corrida de tres horas al 90% y no dejan artefacto. En Windows es peor: un `.pst`
de Outlook abierto hace fallar el run **todas las noches** mientras el usuario tenga el programa
abierto. Política `on_error: fail|skip` por job (default `skip` para files, `fail` para DB), contador
de omitidos en `BackupResult`, rutas omitidas a `run_logs` (la tabla ya existe). **El reporte no es
opcional, es la mitad de la feature**: omitir en silencio hace que alguien crea que respaldó algo
que no respaldó.

---

## 3. Tabla completa (deduplicada, ordenada por impacto)

Las ideas que aparecieron desde dos o más lentes están fusionadas en una sola fila, con los
argumentos sumados. La columna "lente" indica de dónde vino.

| # | Feature | Por qué | Impacto | Esfuerzo | Lente |
|---|---------|---------|---------|----------|-------|
| 1 | Dead-man's switch + notificar desde todos los caminos de falla del scheduler (+ webhook) | El único mail sale de `handler.go:182`; agente caído, destino ilegible, passphrase mala y ticks perdidos son 100 % silenciosos. El modo de falla más caro es la ausencia, no el error | CRÍTICO | M | confiabilidad, dominio, UX, despliegue |
| 2 | Restore disparado desde la UI (completo y selectivo) | No hay endpoint ni `MsgTypeRestore`; recuperar exige SSH al agente, armar `destination.json` a mano y bajar el archivo entero para sacar 4 KB | CRÍTICO | L | UX |
| 3 | DR del control plane: self-backup del Postgres + `export-recovery-kit` | Perder el disco del servidor convierte todos los `.enc` en ruido irreversible: las passphrases sólo viven en esa base | CRÍTICO | M | confiabilidad, despliegue |
| 4 | S3 Object Lock / WORM en el destino | `PutObject` sin retención y `Delete` expuesto: el ransomware que entra al host del agente cifra los datos y borra los backups con las mismas credenciales | CRÍTICO | M | seguridad |
| 5 | El agente nunca ve las credenciales del destino (URL presignada) | `DispatchJob` manda el config S3 **descifrado** por WS; los destinos son globales, así que un agente comprometido entrega las llaves de los backups de toda la flota | CRÍTICO | M | seguridad |
| 6 | Salud por job y por agente (último éxito, antigüedad, próximo tick) | `handleListJobs` no toca `backup_runs`; `last_run`/`next_run` no existen en ninguna parte del repo. Sin esto, las alertas avisan de algo que después no se puede inspeccionar | ALTO | S | confiabilidad, UX |
| 7 | Patrones de exclusión por job | Sin exclusiones, `/var/www` arrastra `node_modules` y `.git`: 4× de storage, ventana y tiempo de restore por datos regenerables | ALTO | S | dominio, UX |
| 8 | Tolerancia configurable a archivos ilegibles, con reporte | Un `.pst` abierto o un temporal borrado a mitad de camino mata un run de 3 h al 90 % y no deja artefacto | ALTO | S | dominio |
| 9 | Retención GFS (diarios/semanales/mensuales/anuales) | `retention_days` es el único parámetro; conservar 12 puntos útiles de un año hoy cuesta 365 fulls (12× el costo de S3) | ALTO | M | dominio |
| 10 | Verificación periódica del artefacto (re-hash + restore dry-run) | El checksum se calcula al subir y nadie lo vuelve a mirar; un objeto truncado o movido a Glacier se descubre el día del incendio | ALTO | M | dominio |
| 11 | `/readyz` con chequeo real + `/metrics` Prometheus | `/healthz` es un literal de tres líneas: responde 200 con Postgres muerto. `grep prometheus` → 0 en todo el repo | ALTO | S | confiabilidad |
| 12 | Catch-up de ventanas perdidas (política de misfire) | Un reinicio de 6 min a las 2 AM saltea 20 jobs sin dejar rastro; el retry loop arranca con `WHERE status='failed'` y no puede ver un tick inexistente | ALTO | M | confiabilidad |
| 13 | Cambio de contraseña del admin desde la UI (+ `token_version`) | `ON CONFLICT DO NOTHING` hace que cambiar `ADMIN_PASSWORD` no haga nada; el procedimiento real documentado es `DELETE FROM admin_users` a mano | ALTO | S | multiusuario, UX, despliegue |
| 14 | Cuentas de usuario nominales | Una sola credencial compartida; no hay forma de revocar a una persona. El middleware no consulta la DB, así que borrar un usuario no invalida su token (24 h) | ALTO | M | multiusuario |
| 15 | Cifrado en reposo de `backup_jobs.source` | La connection string con la contraseña de la base productiva está en JSONB plano, se devuelve tal cual en `GET /api/jobs` y `rotate-key` no la cubre (el propio comentario del código lo admite) | ALTO | S | seguridad |
| 16 | Allowlist de rutas del lado del agente + hardening del unit systemd | El agente hace lo que el servidor le diga: un `JWT_SECRET` filtrado permite crear un job con source `/` y exfiltrar `/etc/shadow` sin explotar ningún bug. El unit no tiene `User=` ni `ProtectSystem` | ALTO | S | seguridad |
| 17 | Rotación / revocación de la API key de un agente | La única forma de invalidar una key filtrada es borrar el agente, y el `ON DELETE CASCADE` se lleva jobs, runs y logs | ALTO | S | seguridad |
| 18 | Firma de releases y self-update verificado | `selfUpdate` hace `http.Get` → `os.Rename` sobre el ejecutable → `syscall.Exec`, sin verificar nada. `install.sh` **sí** verifica el checksum: la ruta que más se usa es la que no | ALTO | M (S sólo el checksum) | seguridad |
| 19 | Bitácora de auditoría de cambios y disparos manuales | Ninguna traza de quién cambió el destino de un job ni cuándo; el vector de exfiltración más silencioso es reapuntar el bucket y esperar. Vale incluso con un solo usuario | ALTO | M | multiusuario, seguridad |
| 20 | Índice de contenido del backup (manifest por run) | No hay forma de saber qué hay adentro sin bajarlo y descomprimirlo; alguien cambia un path y el job sigue en verde respaldando un directorio vacío | ALTO | M | UX |
| 21 | Dumps de DB completos: globals/roles, varias bases, formato elegible | `pg_dump` plano de una sola base: se restaura el dump en una máquina nueva y la app no arranca porque el usuario no existe. `grep pg_dumpall` → 0 hits | ALTO | M | dominio |
| 22 | Servidor empaquetado: imagen Docker + compose completo | No hay Dockerfile y el compose sólo define Postgres; el agente tiene `install`, el servidor no. `install.sh` sobrescribe el binario del server y no reinicia nada | ALTO | M | despliegue |
| 23 | `nat-backup-agent backup`: crear un backup sin servidor | El agente sabe restaurar solo pero no respaldar solo; no se puede validar credenciales ni sacar un backup de emergencia con el control plane caído | ALTO | M | despliegue |
| 24 | Backups incrementales nivel-1 (mtime+size, cadena full+incr) | Un fileserver de 400 GB que cambia 1 GB sube 400 GB por noche: ~9 h sobre 100 Mbps. Es el techo de tamaño de dataset del producto | ALTO | L | dominio |
| 25 | Comandos pre/post backup por job | Única válvula de escape para motores no soportados, quiesce de contenedores y snapshots LVM. Hoy la salida es un cron externo que el producto no ve | MEDIO | S | dominio |
| 26 | Pausa global / ventana de mantenimiento con expiración | Migrar el NAS exige apagar job por job y acordarse de prenderlos; el clásico garantizado es dejar tres apagados en silencio | MEDIO | S | confiabilidad |
| 27 | Consumo y tendencia de espacio por destino | `size_bytes` sólo se muestra run por run; cuando el SFTP se llena fallan TODOS los jobs de ese destino la misma noche | MEDIO | S | confiabilidad |
| 28 | Secretos desde archivo (`_FILE`) | Los 4 secretos salen sólo de `os.Getenv` y el modo documentado es un `.env` en la raíz del repo — literalmente cómo se filtró el `ENCRYPTION_KEY` | MEDIO | S | seguridad |
| 29 | Duplicar job / aplicarlo a varios agentes + tags de agente | 12 sucursales iguales son 24 formularios con la passphrase tipeada 24 veces; un typo produce un backup irrecuperable que nadie descubre hasta el restore | MEDIO | S (duplicar) / M (bulk) | UX, despliegue |
| 30 | Copia secundaria: más de un destino por job (3-2-1) | Duplicar el job significa releer, comprimir y cifrar 400 GB dos veces; el artefacto ya está stageado en disco, subirlo otra vez es un loop | MEDIO | M | dominio |
| 31 | Rol de solo lectura | Quien sólo necesita ver si el backup corrió recibe la misma sesión que puede hacer `DELETE /api/jobs/{id}` y cascadear todo el historial | MEDIO | M | multiusuario |
| 32 | Tokens de API personales y revocables | Cualquier integración obliga a meter la contraseña compartida del admin en una variable de CI, sin poder revocarla por separado | MEDIO | M | multiusuario |
| 33 | Filtros reales en Runs (job, rango de fechas) + vista por agente | La API filtra por `job_id` y la UI ni siquiera lo expone; con 20 jobs diarios encontrar algo es paginar de a 50 hacia atrás | MEDIO | M | UX |
| 34 | Límite de runs concurrentes por destino + jitter de horario | 40 jobs con `0 2 * * *` arrancan al mismo segundo contra el mismo bucket: timeouts intermitentes que fallan distinto cada noche | MEDIO | M (jitter solo: S) | despliegue |
| 35 | Cobertura de plataformas del agente (darwin/arm64, linux/arm) | Los Mac del estudio y las Raspberry de 32 bits hoy no se pueden respaldar en absoluto; el costo de build es casi cero, el de `install` (launchd) no | MEDIO | M | despliegue |
| 36 | Límite de ancho de banda de subida | El nightly satura el uplink de la oficina y no hay perilla; es un `io.Reader` limitado de ~30 líneas | BAJO | S | dominio |
| 37 | TOTP opcional para el login | Seguro barato contra credencial filtrada, pero hacerlo antes de 13/14/19 es teatro de seguridad | BAJO | S | multiusuario |

### Bugs que bloquean features de esta tabla (no son features, son deuda)

Verificados en el código actual y ya reportados en rondas anteriores de review:

- **S3 sube con un solo `PutObject` sin multipart** (`internal/storage/s3.go:78-92`; `grep multipart internal/storage/` → 0). **Hoy ningún archivo mayor a 5 GB se puede guardar en S3.** Bloquea de hecho las filas 9, 24 y 30 en ese destino.
- **`mysqldump` recibe el DSN como nombre posicional de base** (`db.go:60-61`) y **el "dump" de SQLite es un `io.Copy` crudo** del archivo (`db.go:76-92`), que puede salir desgarrado si alguien escribe durante la copia — lo correcto es `VACUUM INTO`. Arreglar antes de la fila 21 o esa feature se construye sobre arena.
- Los dumps no tienen timeout (`exec.Command` sin contexto, `db.go:28`): un `pg_dump` colgado bloquea el run para siempre. Cerrarlo junto con la fila 25, que exige `exec.CommandContext` de todos modos.
- La sección "Limitaciones conocidas" del README (líneas 249-262) quedó desactualizada tras la remediación: afirma que el progreso es binario, que no se guardan logs de runs y que `POST /api/upload/{run_id}` es código muerto — nada de eso es cierto hoy. Un operador que la lea va a creer que el producto es peor de lo que es.

---

## 4. Agrupado por tema

### Dominio del backup (filas 7, 8, 9, 10, 21, 24, 25, 30, 36)

La lógica del conjunto es de **costo y de techo**. Hoy cada corrida hace un tar completo de todo,
sin exclusiones, con una única política de retención por días. Eso significa tres cosas a la vez:
que el dataset máximo que el producto puede atender está limitado por la ventana nocturna, que el
costo de S3 crece linealmente con la retención, y que la única respuesta honesta a "¿puedo
respaldar el servidor de archivos?" es no. El orden dentro del tema no es negociable: **exclusiones
primero** (S, retorno inmediato, y es prerequisito psicológico de todo lo demás — sin ellas el
primer full sigue siendo absurdo), **GFS después** (baja el costo de conservar histórico largo de
365 archivos a 26), y **incrementales al final** (L, y rompe la promesa actual de `restore --file`
con un solo archivo: la cadena full→incrementales obliga a extender el CLI, y borrar un full por
retención invalida todos sus incrementales, así que GFS tiene que entender la cadena). La
verificación periódica (fila 10) es la que cierra el círculo: el checksum de hoy prueba que el
agente calculó bien un hash localmente, no que lo que quedó en el destino sea eso. La pieza cara ya
está construida — `RestoreWithOptions{DryRun:true}` descomprime, descifra y recorre el tar entero
sin escribir nada.

### Confiabilidad y operación (filas 1, 6, 11, 12, 26, 27)

Todo este tema es una sola tesis: **el sistema detecta fallos ruidosos y es ciego al silencio**. Y
el silencio es el modo de falla más probable y más caro de un gestor de backups. Las filas 1, 6 y
12 comparten literalmente la misma consulta de fondo — "último run exitoso por job contra el tick
esperado del cron" — y por eso conviene hacerlas juntas: una alerta que no se puede inspeccionar en
ninguna pantalla es tan inútil como una pantalla que no alerta. La fila 11 es la palanca más barata
para quien ya tiene monitoreo: `nat_backup_last_success_timestamp_seconds{job=...}` deja que el
operador arme su alerta de staleness en la herramienta donde ya mira todo lo demás, y el sistema
deja de ser una isla. **No reemplaza a la fila 1**: quien no tiene Prometheus necesita igual la
alerta interna, y la interna necesita igual estas métricas para ser verificable.

### Multiusuario y gobierno (filas 13, 14, 19, 31, 32, 37)

El modelo actual es una tabla `admin_users` con cuatro columnas y un único usuario cuya contraseña
no se puede cambiar. El orden correcto acá es **operativo antes que de autenticación**: primero
poder rotar la contraseña (fila 13, S, y hoy es un agujero abierto), después usuarios nominales con
invalidación real de token (fila 14 — sin `token_version` o lookup por request, borrar un usuario
no lo echa: su JWT sigue entrando hasta 24 h después), después auditoría (fila 19, que aporta valor
incluso con un solo usuario porque el "qué cambió y cuándo" ya diagnostica el drift de
configuración). Los roles y los tokens de API cuelgan de la fila 14 y no tienen sentido sin ella.
TOTP va último y en BAJO a propósito: la fuerza bruta ya está cubierta por el rate limit de 10/min
por IP, y el agujero real de este sistema no es autenticar mejor sino no poder revocar ni saber
quién hizo qué.

### Experiencia de usuario (filas 2, 6, 20, 29, 33)

El patrón de fondo: **el producto sabe hacer backups y sabe casi nada de recuperarlos desde la
interfaz**, que es la única razón por la que existe. La fila 2 es la que cierra ese hueco y es la
más cara de todo el roadmap; la fila 20 (manifest) es su habilitante directo — sin índice, elegir
un archivo en la UI obliga a streamear el archivo entero sólo para listarlo, porque tar no tiene
índice interno. Las filas 6, 29 y 33 son ergonomía pura y baratas: mostrar el estado donde el
usuario ya mira, no obligarlo a llenar el mismo formulario doce veces, y poder filtrar por el
servidor concreto que le preocupa ("el server de facturación", no "el job 3f2a1b").

### Seguridad y cumplimiento (filas 4, 5, 15, 16, 17, 18, 28)

Dos bloques distintos. El primero es **contención del agente**: hoy el servidor le manda al agente
las credenciales completas del destino (descifradas, por WS) y el agente ejecuta cualquier path que
le pidan, corriendo como root sin ningún hardening. Las filas 5 y 16 juntas convierten "un agente
comprometido entrega las llaves de todos los backups de toda la flota" en un incidente acotado a esa
máquina. El segundo bloque es **inmutabilidad**: la fila 4 es la única defensa real contra el
playbook de ransomware estándar desde 2019, que borra los backups en el mismo movimiento en que
cifra los datos. Ojo con la interacción: `retention.go:70` reintenta el borrado en cada pasada, así
que con Object Lock hay que marcar esos runs como protegidos en vez de loguear un error para
siempre; y el `retention_days` del job debe ser ≥ el lock del bucket o la política queda
desalineada con la realidad. Las filas 15, 17, 18 y 28 son cierres puntuales de agujeros
verificados, todos de esfuerzo bajo salvo la firma de releases.

### Despliegue y escala (filas 3, 22, 23, 34, 35)

A 30–50 agentes el cuello de botella **no es el servidor**: el hub es un map con RWMutex y dos
goroutines por conexión, el pool de Postgres está acotado en 25, y robfig/cron con 2000 entradas no
transpira. Los límites reales son de red y de destino compartido (fila 34) y de horas-persona para
administrar los jobs (fila 29). Lo que sí falta y duele es que el componente cuya caída apaga TODOS
los backups de TODAS las máquinas es el único que no tiene instalación empaquetada (fila 22) y el
único que no se respalda a sí mismo (fila 3). La fila 23 (`agent backup` standalone) es la que hace
del binario del agente una herramienta útil por sí sola — que es exactamente lo que lo hace
confiable — y de paso habilita correr el dump del propio Postgres del servidor desde cron sin
depender de que el servidor esté vivo.

---

## 5. Qué NO hacer

### Descartado por ya existir

**Ninguna de las 41 propuestas recibidas duplica funcionalidad ya implementada.** Lo verifiqué
propuesta por propuesta para todo lo marcado CRÍTICO o ALTO. Dejo constancia de lo que **sí existe**
y por lo tanto no aparece en la tabla, porque la remediación reciente lo cubrió:

- Restauración por CLI: `cmd/agent/restore.go` + `internal/backup/restore.go` (dry-run, magic bytes, anti-traversal).
- Retención con borrado real en el destino: `internal/notify/retention.go` usando `Backend.Delete`.
- Reintentos con backoff persistidos y guarda de solapamiento: `internal/scheduler/scheduler.go` + migración 004 (índice parcial `idx_backup_runs_one_running_per_job`).
- Checksum SHA-256 del artefacto: migración 005 + `cmd/agent/main.go:396`.
- Progreso real con throttling: `internal/backup/progress.go`.
- Logs de run persistidos y streaming por WS: migración 003 + `handleRunLogsWS`.
- Paginación y stats agregadas: `internal/api/runs.go`.
- Cifrado en reposo de configs de storage y passphrases, con rotación de llave: `internal/crypto` + `cmd/rotate-key`.
- Rate limiting del login (10/min por IP) y del socket de consola.
- Verificación de host key en SFTP.
- Reconciliación de runs zombie al arrancar (`cmd/server/main.go:65-72`).
- Rollback de migraciones con detección de schema dirty (`internal/db/migrate.go`).
- Self-update del agente y release con SHA256SUMS (aunque el self-update **no** lo verifica — fila 18).

### Descartado por no valer la pena en este contexto

- **Alta disponibilidad del servidor.** Dos instancias contra la misma DB ya no producirían runs
  duplicados (`BeginRun` + el índice parcial de la migración 004 lo impiden), pero pelearían por
  `reconcileOffline`, que marca offline a todo agente que la otra instancia tenga conectado. Montar
  HA para una PyME con 30 servidores es desproporcionado: el daño real de "el server se cayó" no es
  la falta de failover, es que la ventana de backup se pierde en silencio — y eso lo resuelve la
  fila 12 (catch-up) por una fracción del costo. Si algún día corren dos instancias, un
  `pg_try_advisory_lock` alrededor de `Scheduler.Start` y del reconciliador es un parche de horas.
- **Deduplicación por chunking estilo restic/borg.** Es otro producto: índice, repositorio,
  compactación, locking. No cabe en el diseño actual de "un archivo por run en un destino tonto".
  El incremental nivel-1 de la fila 24 da la mayor parte del beneficio sin ese rediseño.
- **SSO / SAML / OIDC.** Una PyME de 4 personas que se autohospeda casi seguro no tiene IdP, y si lo
  tuviera, integrar OIDC cuesta más que el problema que resuelve. Si alguna vez hace falta, el
  servidor va detrás de un proxy autenticador (Authelia / oauth2-proxy): eso es configuración, no
  código.
- **Lista negra de `jti` / logout server-side.** El token dura 24 h y hay un solo usuario. El
  `token_version` de la fila 14 cuesta lo mismo y además resuelve el offboarding, que es el problema
  real.
- **Archivo de configuración del servidor y recarga en caliente.** Con 9 variables de entorno y la
  config de SMTP ya leída de la DB en cada alerta, un archivo de config es ceremonia sin beneficio.
  Lo único que hoy exige reinicio y no debería es la contraseña de admin, y eso es la fila 13.
- **Compresión y cifrado configurados por destino en vez de por job.** Hoy viven en `backup_jobs` y
  eso es lo correcto: quien decide si un dump va cifrado es el dueño del dato, no el bucket.
- **App móvil o push nativo.** No se justifica para un solo admin. El webhook a Telegram/ntfy de la
  fila 1 da el 100 % del beneficio por el 2 % del costo.
- **Estimación previa de tamaño y duración del backup.** Con `file_count` y `size_bytes` históricos
  ya guardados se deriva en el cliente sin backend nuevo; valor bajo frente a todo lo demás.
- **Matriz de permisos granular.** Con dos roles (admin / viewer) alcanza para 4 personas.
- **FreeBSD / TrueNAS como target del agente.** Sólo bajo pedido explícito.

### Descartado con matiz

- **La fila 4 (Object Lock) es CRÍTICO pero no entró en el top 5.** Razón explícita: sólo aplica a
  S3, exige que el bucket se haya creado con Object Lock habilitado (no se puede activar después) y
  con `COMPLIANCE` ni el root de la cuenta puede borrar antes del vencimiento — es una decisión de
  infraestructura del cliente tanto como una feature. Y proteger backups de los que no sabés si
  existen (fila 1) ni podés abrir (fila 3) es proteger lo equivocado primero.
- **La fila 35 (plataformas) es la propuesta más floja del roadmap** y se dice con esa etiqueta. Si
  el parque real es "servidores Linux + algún Windows Server", no mueve la aguja. Está sólo porque
  el costo marginal de los targets de build son líneas en un YAML, y porque hoy es un "no" duro, no
  un "anda peor". `darwin/arm64` primero, `armv7` después.

---

## 6. Orden sugerido

**Bloque 0 — deuda que bloquea (antes de cualquier feature de storage)**
S3 con multipart upload. Sin esto, GFS, incrementales y multi-destino no se pueden ni probar en el
destino que más se usa, porque hoy nada mayor a 5 GB entra en S3.

**Bloque 1 — dejar de ser ciego (el mismo sprint)**
Filas 1 + 6. Comparten la consulta de freshness; hacerlas juntas duplica el valor por el mismo
backend. Empezar por la mitad barata de la fila 1 (cablear el notifier al scheduler, ~medio día)
porque cierra hoy el agujero más grande. Sumar la fila 11 (`/readyz` + `/metrics`, S) en el mismo
bloque: desbloquea a cualquier operador que ya tenga monitoreo externo y hace verificable a la fila
1.

**Bloque 2 — poder recuperarse (independientes entre sí, ambos CRÍTICO)**
Fila 3 (DR del control plane) y fila 2 (restore desde la UI). La 3 primero si hay que elegir: es M
contra L, y su ausencia es la única que destruye retroactivamente el trabajo de años. La fila 20
(manifest) va pegada a la fila 2 porque es su habilitante para el restore selectivo.

**Bloque 3 — bajar el costo del backup (todo S)**
Filas 7 (exclusiones) y 8 (tolerancia a ilegibles). Se pueden hacer en paralelo con el bloque 2, son
del motor del agente y no tocan el servidor. La 7 antes que cualquier cosa de retención o
incrementales.

**Bloque 4 — gobierno mínimo**
Fila 13 (cambiar la contraseña, S, agujero abierto hoy) → fila 14 (usuarios nominales con
`token_version`) → fila 19 (auditoría). En ese orden estricto: la 19 sin la 14 registra el "qué" pero
no el "quién", y aun así vale; la 14 sin invalidación de token miente.

**Bloque 5 — contener al agente**
Filas 16 (allowlist + hardening systemd, S), 15 (cifrar `source`, S), 17 (rotar API key, S), 18
(verificar el self-update). Las cuatro son cierres puntuales y baratos; la 18 en dos pasos, primero
el checksum (S, cierra MITM) y después la firma (M, cierra el vector real) — y decir claramente que
sólo el primero es un parche parcial.

**Bloque 6 — retención y verificación**
Fila 9 (GFS) y después fila 10 (verificación periódica). GFS antes que incrementales, siempre: la
fila 24 depende de que la retención entienda la cadena full→incremental, o borrar un full invalida
en silencio todos sus incrementales.

**Bloque 7 — operación a escala**
Filas 22 (Docker/compose), 23 (`agent backup`), 12 (catch-up), 26 (pausa global), 27 (tendencia de
espacio), 29 (duplicar job). Todas MEDIO o baratas; el orden dentro del bloque lo dicta el dolor que
aparezca primero.

**Bloque 8 — techo de dataset**
Fila 24 (incrementales, L). Sólo después de 7, 9 y del multipart de S3. Aceptar de frente los tres
riesgos antes de empezar: restaurar pasa a requerir la cadena completa, la retención tiene que
entenderla, y `mtime` es engañable — hace falta un full forzado periódico.

**Al final**: filas 30, 31, 32, 33, 34, 35, 36, 37.
