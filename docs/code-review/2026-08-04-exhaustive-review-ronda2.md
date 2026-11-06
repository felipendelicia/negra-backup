# Review exhaustivo de nat-backup — Ronda 2 (lentes transversales) — 2026-08-04

Repo: `/home/felipe/Documents/Repositories/nat-backup` · Branch: `main` · HEAD: `4de649c`
Informe de la ronda 1: [`2026-08-04-exhaustive-review.md`](./2026-08-04-exhaustive-review.md)

---

## 0. Qué es esta ronda y cómo leerla

La ronda 1 barrió el repo **por capas** (api-http, ws-scheduler, backup-storage-crypto, cmd-agent-server, web-ui, ops-ci-docs, gaps) y produjo 218 findings crudos / ~184 únicos. Esta ronda barrió el mismo código con **8 lentes transversales**, cada una atravesando todas las capas con una sola pregunta:

| Lente | Pregunta |
|---|---|
| `attacker` | ¿Qué puede hacer alguien con acceso parcial? ¿Dónde están las cadenas de privilegio? |
| `data-path` | Seguir un byte desde el archivo origen hasta el artefacto cifrado y de vuelta |
| `race-repro` | Escribir tests que **reproduzcan** carreras, no razonar sobre ellas |
| `state-machine` | ¿Qué transiciones ilegales acepta el sistema? ¿Qué invariantes se rompen? |
| `failure-injection` | ¿Qué pasa cuando falla la DB, la red, el disco, el proceso? |
| `tests-lie` | ¿Qué tests pasan sin verificar nada? ¿Qué mentira cuenta la cobertura? |
| `build-embed-release` | Del commit al binario en el host: reproducibilidad, embedding, distribución |
| `operator-ux` | El día a día de quien opera esto: ¿qué es imposible, confuso o mentiroso? |

**Los duplicados contra la ronda 1 ya fueron filtrados por el pipeline.** Lo que queda son 60 findings únicos.

### Caveat (idéntico al de la ronda 1, y aplica igual)

- Los **P1 pasaron verificación adversarial**: los seis están marcados `CONFIRMED` y cuatro de ellos se reprodujeron ejecutando código (`go test`, `objdump`, `httptest`). Aun así, **confirmá cada finding contra el código antes de escribir el fix**: las líneas citadas corresponden a `4de649c`.
- El verificador adversarial **recortó dos severidades que el pipeline había marcado P0** (`cron.Recover` y el diálogo de borrar job). Este informe usa la severidad del verificador, no la del pipeline. **Esta ronda no produjo ningún P0.**
- Los P2/P3 marcados `—` en la columna **V** se reportan por lectura de código, sin verificación adversarial. Tratalos como hipótesis fundamentadas, no como hechos.
- Nada se ejecutó contra un Postgres real, un agente real, un endpoint S3/SFTP real ni un host Windows. Ver sección 8.

---

## 1. Qué agrega esta ronda

1. **Un panic conocido de la ronda 1 mata el proceso por una vía que nadie había mirado.** El `send on closed channel` de `hub.go:168` es inocuo cuando sale del read-loop del agente (chi `Recoverer` lo atrapa) y **fatal** cuando sale del scheduler: `cron.New()` no instala `cron.Recover`, y en robfig/cron v3 ese wrapper es opt-in. Reproducido: `panic: ... created by (*Cron).startJob in goroutine 9`, proceso muerto. Sin unit de systemd para el server (`install.sh` dice literalmente "ejecutar: nat-backup-server"), el crash es outage permanente. **Un backup a las 2 AM que coincida con un agente desconectándose tira el servidor.**

2. **El agente se congela entero, y el culpable es el logger.** `writeJSON` no pone `SetWriteDeadline` (el server sí, `hub.go:313`) y retiene `a.mu` durante todo el `WriteJSON`. Como `log.SetOutput` cablea todo `log.Printf` hacia ese mismo `writeJSON`, un write clavado congela el mutex del paquete `log` y con él **cada goroutine del agente que loguee**. Reproducido con salida literal: `log.Printf blocked >2s`, `heartbeat writeJSON blocked`. Dos lentes independientes llegaron al mismo lugar.

3. **Los tests que deberían proteger lo más crítico no pueden fallar.** El único test que guarda el cifrado (`TestBackupFiles_WithEncryption`) afirma que el plaintext no aparece en la salida — y eso **también es cierto sin cifrar**, porque zstd ya destruye la cadena. Verificado empíricamente: con `Encrypt:false` y con `Encrypt:true, Passphrase:""` el assert pasa igual, y ambos producen exactamente 155 bytes. Es decir: el test estaba verde mientras el P0 de la passphrase vacía shippeaba, y seguiría verde con el cifrado arrancado de raíz.

4. **Los fixes de este informe no van a llegar a los hosts.** Ni `install.sh` ni `install-agent.ps1` reinician el servicio después de reemplazar el binario. En Linux el `mv` sobre un ejecutable en uso funciona y systemd sigue corriendo el inode viejo **indefinidamente** mientras el script imprime "instalado correctamente"; en Windows el `Move-Item` falla con acceso denegado y el script muere a mitad. `grep -rn 'restart|systemctl|Restart-Service' scripts/` devuelve un único hit, y es un `echo`. Este finding es meta-bloqueante: sin arreglarlo, cualquier remediación de las otras 59 es teórica.

5. **La lente `attacker` no encontró ningún P0/P1 — y eso es un resultado, no un hueco.** Verificó el mapa completo de rutas contra middlewares y no halló: ninguna ruta montada fuera de un grupo autenticado, ningún IDOR (el check de `upload.go:34-47` está bien puesto), ningún path traversal en el upload (`filepath.Base` neutraliza), ninguna inyección de headers SMTP, ningún XSS (cero `dangerouslySetInnerHTML` en `web/src`), ningún bug de `alg=none` en el JWT del Console. **La superficie de autenticación y autorización de la ronda 1 quedó bien cubierta.** Lo único nuevo de esa lente es hardening y observabilidad.

6. **Tres lentes convergieron por separado en el mismo agujero de estado, y una cuarta en el mismo de esquema.** `state-machine`, `failure-injection` y `operator-ux` encontraron independientemente que `job_done` no tiene la guarda de `status='cancelled'` que `job_failed` sí tiene; `data-path`, `state-machine` y `operator-ux` encontraron que `backup_runs.storage_path` guarda solo un basename y la fila no tiene `storage_destination_id`. Convergencia desde ángulos distintos = alta confianza.

### 1.1 Bonus operativo: cómo descifrar un artefacto a mano, hoy

La lente `data-path` reconstruyó el formato del contenedor cifrado leyendo `internal/backup/encrypt.go`. Esto **no arregla** el P1 de la ronda 1 ("No existe restauración de backups"), pero es la salida de emergencia que hoy no está documentada en ningún lado:

```
[0:32]   salt aleatorio
luego, repetido hasta EOF:
         [4 bytes] uint32 big-endian = largo del chunk sellado
         [12 bytes] nonce
         [resto]    ciphertext + tag GCM de 16 bytes
key = PBKDF2-HMAC-SHA256(passphrase, salt, 600_000 iteraciones, 32 bytes)
plaintext = tar comprimido con zstd o gzip (magic: 28 b5 2f fd / 1f 8b)
```

Lo que **no** está en el archivo y hay que adivinar: que el KDF es PBKDF2-SHA256 con exactamente 600.000 iteraciones (si esa constante cambia en una versión futura, los backups viejos fallan con `decrypt chunk`, indistinguible de passphrase incorrecta), si el contenido es zstd o gzip, y el motor de DB si era un dump. **Conclusión: el artefacto es recuperable con un script de ~30 líneas escrito contra el código fuente de esta versión exacta, e irrecuperable sin ese código fuente.** Guardar una copia de `internal/backup/encrypt.go` junto a los backups es, hoy, parte del plan de recuperación.

---

## 2. Conteo por lente y severidad

60 findings únicos. **0 P0 · 6 P1 · 41 P2 · 13 P3.**

| Lente | P1 | P2 | P3 | Únicos | Dup. ronda 1 (declarados) | Hipótesis falsificadas / negativos |
|---|---:|---:|---:|---:|---:|---:|
| attacker | 0 | 3 | 1 | **4** | 5 | 7 |
| data-path | 0 | 6 | 4 | **10** | 3 | 5 |
| race-repro | 2 | 2 | 1 | **5** | 3 | 6 |
| state-machine | 0 | 3 | 2 | **5** | 6 | 4 |
| failure-injection | 1 | 3 | 2 | **6** | 4 | 3 |
| tests-lie | 1 | 9 | 1 | **11** | 0 | 6 |
| build-embed-release | 1 | 8 | 1 | **10** | 6 | 8 |
| operator-ux | 1 | 7 | 1 | **9** | 13 | 5 |
| **Total** | **6** | **41** | **13** | **60** | **40** | **44** |

Las columnas de duplicados y falsificados son **lo que cada lente declaró explícitamente en sus notas**, no un conteo del pipeline. Valen como señal de calidad de la ronda 1: 40 hallazgos fueron descartados por estar ya reportados, y 44 hipótesis plausibles resultaron falsas al verificarlas.

**Fusiones dentro de esta ronda:** 14 findings de lentes distintas describían 6 defectos. Quedan fusionados (marcados `(N lentes)` en las tablas) y las 8 entradas redundantes no se cuentan.

---

## 3. Findings P1

> Esta ronda no produjo ningún P0. Los seis P1 están `CONFIRMED`.

### [P1][Reliability] `cron.New()` sin `cron.Recover`: el mismo panic que es inocuo en un handler HTTP mata el proceso cuando sale del scheduler — **NUEVO**

**Location:** `internal/scheduler/scheduler.go:31` · camino `scheduler.go:143` → `internal/ws/hub.go:168`
**Problem:** El `send on closed channel` de `DispatchJob` (el P0 de la ronda 1, "`panic: send on closed channel` en los tres hubs") es alcanzable desde dos goroutines con **radio de explosión opuesto**, y la ronda 1 no distinguió. Reproducido A: desde el read-loop del agente (`handler.go:137` → `ServeHTTP` → chi `Recoverer` → `net/http`) el panic se dispara 10+ veces en 3 s y el proceso **sobrevive siempre**; solo muere esa conexión WS. Reproducido B: desde el scheduler, `cron.New()` no instala el `JobWrapper` `cron.Recover` — que en robfig/cron v3.0.1 es **opt-in** (`chain.go:38`) y cuyo `startJob` (`cron.go:308-314`) es un `go func(){ defer c.jobWaiter.Done(); j.Run() }()` sin ningún `recover` — así que el panic desenrolla y **mata el proceso**: `panic: send on closed channel ... cron/v3.(*Cron).startJob.func1() cron.go:312 ... created by (*Cron).startJob in goroutine 9`. Un backup programado que coincida con un agente desconectándose tira el servidor completo, y lo mismo cualquier panic futuro dentro de `triggerJob` (decrypt, driver de DB, nil deref). **Agravante:** no hay supervisor — `scripts/install.sh:105-108` dice literalmente `3. Ejecutar: nat-backup-server`, sin unit systemd ni `Restart=`, así que el crash es un outage permanente hasta intervención humana.
**Proposal:** `cron.New(cron.WithChain(cron.Recover(cron.DefaultLogger), cron.SkipIfStillRunning(cron.DefaultLogger)))` — el segundo wrapper cierra además el P1 de solapamiento de la ronda 1. Envolver con `defer/recover` las demás goroutines desnudas sin protección HTTP: `go hub.Run()` (`server.go:44,65`), `go h.notifyFailure(...)` (`handler.go:167`) y el scheduler de retención. Y agregar un unit de systemd para el server con `Restart=on-failure`.
**Effort:** S · **Risk:** low · **Verificado:** CONFIRMED (reproducido: `go test ./internal/scheduler/ -run TestPanic_CronNewHasNoRecover`)
**Nota de severidad:** el pipeline lo marcó P0; el verificador lo recortó a **P1 porque es amplificador, no causa raíz** — `cron.Recover` convierte el crash en un dispatch perdido en silencio, no arregla el bug de fondo (que es el P0 de la ronda 1). Tres findings distintos viven ahora en `scheduler.go:31`: este, "Cron sin timezone explícita" (P2, ronda 1) y "Sin protección de solapamiento" (P1, ronda 1). **Arreglarlos juntos.**

---

### [P1][Reliability] El agente nunca pone write deadline al WebSocket: un write stalleado congela el proceso entero, incluido todo el logging — **NUEVO** · *(2 lentes: `race-repro` + `failure-injection`)*

**Location:** `cmd/agent/main.go:84-91` (`writeJSON`, único camino de escritura del agente) · `main.go:126` (`log.SetOutput`) · `main.go:362-369` (`wsLogWriter`)
**Problem:** `grep -rn SetWriteDeadline cmd/agent/` no devuelve nada; el servidor sí lo pone (`internal/ws/hub.go:313,318`, 10 s). `writeJSON` retiene `a.mu` durante **todo** el `WriteJSON` de red, y `main.go:126` hace `log.SetOutput(io.MultiWriter(os.Stderr, &wsLogWriter{agent: a}))`, de modo que **cada `log.Printf` del agente termina dentro de ese write con el mutex del paquete `log` tomado**. Contra un servidor que hace Upgrade y jamás lee (sobrecarga, ventana TCP en cero, blackhole sin RST), reproducido literalmente: `writeJSON is still blocked after 3s with no deadline and no error`, `log.Printf blocked >2s`, `heartbeat writeJSON blocked`. El dump confirma la cadena: goroutine en `wsLogWriter.Write (main.go:367) → Agent.writeJSON (main.go:85)` bloqueada en `a.mu` **con el mutex de `log` tomado**, y otra en `log.(*Logger).SetOutput` esperando ese mismo mutex. Efecto: el agente no puede loguear, ni mandar `job_progress`, ni completar la limpieza de `connect()` (`main.go:104-110` también bloquea en `a.mu`), y el server lo da por caído a los 60 s mientras el proceso sigue vivo y zombie.
**Proposal:** (1) `a.conn.SetWriteDeadline(time.Now().Add(10*time.Second))` dentro de `writeJSON`, antes del `WriteJSON`. (2) Sacar el log del camino crítico: `wsLogWriter` debe empujar a un canal buffereado con drop-on-full, drenado por una goroutine escritora, para que `log.Printf` **nunca** pueda tomar `a.mu`.
**Effort:** S · **Risk:** low · **Verificado:** CONFIRMED (reproducido: `go test ./cmd/agent/ -run TestAgentFreeze`; el propio test se colgó hasta el timeout con `log.(*Logger).SetOutput` en `sync.Mutex.Lock` — prueba extra)
**Matices honestos:** contra el server *real* la congelación no es permanente — `handler.go:89` pone `SetReadDeadline(60s)`, el read loop sale, el `defer Unregister` cierra `ac.send` y `writePump` hace `defer ac.conn.Close()` (`hub.go:305`), lo que desbloquea el write en ~60 s. Y "ni atender `cancel_job`" es indirecto: el read loop (`main.go:153`) no toma `a.mu` y sí procesa el frame — se traba recién en el `log.Printf("cancelling run %s")` de `main.go:186`, por el mutex del log.

---

### [P1][Bug] Cancelar un run responde 204 aunque el agente nunca reciba el cancel, y después `job_done` lo resucita como `success` — **NUEVO** · *(3 lentes: `failure-injection` + `state-machine` + `operator-ux`)*

**Location:** `internal/api/runs.go:35-54` · `internal/ws/handler.go:139-150` vs `152-158`
**Problem:** Tres eslabones, los tres literales. (1) `handleCancelRun` marca la fila `'cancelled'` **antes** de intentar avisar al agente (`runs.go:35-42`) y **descarta el bool** que devuelve `hub.CancelJob` (`runs.go:50`), que es `false` si el agente está desconectado o si su canal de 64 está lleno (`hub.go:242-259`); responde `204` incondicionalmente (`runs.go:54`). El usuario cree que frenó el backup. (2) La goroutine del job en el agente (`main.go:173-181`) no está atada al ciclo de vida de la conexión: sobrevive una desconexión, sube el archivo (`main.go:318`) y manda `job_done` por la conexión nueva. (3) El handler de `MsgTypeJobDone` (`handler.go:139-149`) hace `UPDATE backup_runs SET status='success', finished_at=NOW(), ...` **sin ninguna guarda** — mientras que `MsgTypeJobFailed` sí tiene la suya (`handler.go:156`: `... && currentStatus == "cancelled" { break }`). Además el UPDATE no toca `error_message`, así que la fila queda `status='success'` con `error_message='Cancelled by user'`, que `web/src/pages/Runs.tsx:165` pinta en rojo debajo del badge verde.
**Proposal:** (a) `AND status='running'` en el UPDATE de `job_done` (y reemplazar el `SELECT`+`break` de `job_failed` por la misma guarda atómica), limpiar `error_message` en el éxito y loguear cuando `RowsAffected()==0`. (b) Devolver 409/202 según el bool de `CancelJob`, o marcar `'cancelling'` hasta que el agente confirme, en vez de marcar `'cancelled'` unilateralmente. El endpoint muerto `POST /api/upload` ya implementa exactamente estas dos guardas (`upload.go:29-32` estado `running` + `34-47` ownership); **el path vivo no tiene ninguna**.
**Effort:** S · **Risk:** low · **Verificado:** CONFIRMED
**Alcance real (no inflar):** el camino *normal* de cancelación emite `job_failed("cancelled")`, que **sí** está guardado — los checkpoints de `cmd/agent/main.go:238,250,270,293` abortan antes del `Upload`. La ventana de resurrección es la que va desde `main.go:293` en adelante (cancel llegando durante el upload, o después de que el agente ya terminó) más el caso del agente desconectado. No hay pérdida del backup: el archivo efectivamente se subió. El daño es historial mintiendo y un cancel que no cancela.

---

### [P1][Testing] El único test que guarda la feature de cifrado pasa igual **sin cifrado** — **NUEVO** (amplía el P0 "Passphrase vacía produce un backup SIN cifrar" de la ronda 1)

**Location:** `internal/backup/files_test.go:32-47`, assert en `:46`
**Problem:** `TestBackupFiles_WithEncryption` cifra y afirma `assert.NotContains(t, buf.String(), "sensitive data")`. Ejecutado el mismo escenario con las tres variantes (copia del repo en scratchpad, original intacto):

```
Encrypt:true  pass=my-passphrase zstd → out=219  containsPlaintext=false  assert PASA
Encrypt:false                    zstd → out=155  containsPlaintext=false  assert PASA
Encrypt:true  pass=EMPTY         zstd → out=155  containsPlaintext=false  assert PASA
Encrypt:false                    gzip → out=155  containsPlaintext=false  assert PASA
```

El assert **no puede distinguir cifrado de simplemente comprimido**: zstd/gzip ya destruyen la cadena literal. Nótese que `Encrypt:false` y `Encrypt:true, Passphrase:""` producen salidas de **exactamente el mismo tamaño** (155 bytes) — prueba directa del P0 de la ronda 1 (`files.go:50`, `if cfg.Encrypt && cfg.Passphrase != ""`). El test es literalmente incapaz de fallar, y estaba verde mientras ese P0 shippeaba.
**Proposal:** Round-trip real: `DecryptStream(buf, &out, passphrase)` y comparar contra la salida no cifrada. Agregar el caso `Encrypt:true, Passphrase:""` afirmando `require.Error` (hoy devuelve `nil` y produce un tar en claro con extensión `.enc`). Alternativa mínima de una línea: verificar el prefijo de salt de 32 bytes (`encrypt.go:16,32`).
**Effort:** S · **Risk:** low · **Verificado:** CONFIRMED (ejecutado)
**Por qué es P1 y no P2:** no es "falta cobertura" — es que **existe un test que dice cubrir la propiedad de seguridad central del producto y no la cubre**. Ese es el peor estado posible: peor que no tener test, porque bloquea la sospecha.

---

### [P1][Reliability] Los instaladores reemplazan el binario pero nunca reinician el servicio: en Linux se sigue corriendo el código viejo, en Windows el update aborta — **NUEVO**

**Location:** `scripts/install.sh:64,72-74` · `scripts/install-agent.ps1:56-58`
**Problem:** `install.sh` detecta la versión instalada, imprime `Actualizando $name $CURRENT → $LATEST...` (`:64`), reemplaza el binario con `mv "$TMP" "$dest"` (`:72-73`) y declara `ok "$name instalado en $dest"` (`:74`). **Nunca ejecuta `systemctl restart`.** `grep -rn 'restart|systemctl|Restart-Service|Stop-Service' scripts/` devuelve **un único hit**: `install.sh:116`, dentro de un `echo` de "próximos pasos" para la primera instalación, que no cubre el path de update. En Linux el rename sobre un ejecutable en uso funciona, así que el proceso systemd (unit generado en `cmd/agent/main.go:411-421`, con `Restart=always` — que solo actúa si el proceso muere, y nada lo mata) **sigue corriendo la imagen vieja indefinidamente** mientras el usuario cree que actualizó. En Windows, `Move-Item -Force $Tmp $BinPath` (`install-agent.ps1:58`) sobre un `.exe` en ejecución falla con acceso denegado y, con `$ErrorActionPreference = "Stop"` (`:15`), el script muere a mitad dejando el binario viejo.
**Proposal:** En `install.sh`: si `systemctl is-active nat-backup-agent` da activo, reiniciarlo tras el `mv` y verificar que arrancó. En el `.ps1`: `Get-Service` → `Stop-Service` antes del `Move-Item` → `Start-Service` después. Reportar la versión efectivamente corriendo al final, no la descargada.
**Effort:** S · **Risk:** low · **Verificado:** CONFIRMED
**Precisión adicional:** `$TMP` viene de `mktemp` (`:70`), así que si `/tmp` y `/usr/local/bin` están en filesystems distintos, GNU `mv` degrada a copy-over-destination y falla con `ETXTBSY` sobre un ejecutable en uso, abortando por `set -euo pipefail` (`:11`). En ambas ramas el update **no toma efecto**; solo cambia si falla ruidosamente o en silencio.
**Por qué importa más de lo que parece:** es meta-bloqueante. Un operador que aplique los fixes de seguridad de este informe y de la ronda 1, y después corra el instalador, va a creer que parcheó agentes que manejan credenciales de storage y passphrases de cifrado — y no parcheó nada.

---

### [P1][DataLoss] El diálogo de borrar job promete conservar el historial y la FK lo borra en cascada — **NUEVO**

**Location:** `web/src/pages/Jobs.tsx:469` · `migrations/001_initial_schema.up.sql:42` · `internal/api/jobs.go:149`
**Problem:** El `ConfirmDialog` dice literalmente:

```
Delete job "${deleteTarget?.name}"? All run history will be preserved.
```

y `backup_runs.job_id` es `UUID NOT NULL REFERENCES backup_jobs(id) ON DELETE CASCADE`, así que el `DELETE FROM backup_jobs WHERE id=$1` de `jobs.go:149` **destruye todos los runs de ese job**. No hay borrado previo ni preservación de ningún tipo. El operador borra un job creyendo que conserva la evidencia de meses de backups y pierde la única traza de qué se respaldó, cuándo y dónde (`storage_path` incluido), mientras los archivos siguen ocupando el destino sin ningún registro que los relacione — y la retención nunca los borra (P1 de la ronda 1).
**Proposal:** Cambiar la FK a `ON DELETE RESTRICT` y ofrecer archivar / soft-delete del job; o —si se acepta el cascade— reescribir el texto a "Se eliminarán también los N runs de este job" mostrando el conteo real antes de confirmar. **La afirmación actual es lo opuesto exacto de lo que hace la DB.**
**Effort:** S · **Risk:** low · **Verificado:** CONFIRMED
**Distinto del finding de la ronda 1:** aquel ("El diálogo de borrar agente miente y oculta el borrado en cascada de jobs y runs", `Agents.tsx:386`) es una **omisión** — el texto real es `Remove agent "X"? This will not stop the agent process — it will re-register on next connection.`, que no menciona el cascade. Este es una **afirmación falsa activa**. `Jobs.tsx:469` no aparece en ninguna línea del informe de la ronda 1.
**Nota de severidad:** el pipeline lo marcó P0; el verificador lo recortó a **P1** porque la pérdida es de metadatos de auditoría, no de los backups, y se dispara solo con una acción destructiva explícita del operador.

---

## 4. Findings P2 y P3, por lente

Leyenda **V**: ✓ = verificado con evidencia citada (`CONFIRMED` o `ADJUSTED`) · — = **sin verificar**, reportado por lectura de código.

### L1. `attacker` — cadenas de privilegio, superficie de auth

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | Las API key de agente no se pueden rotar ni revocar; borrar el agente no corta la sesión WS viva, y perder la key obliga a borrar el agente *(2 lentes)* | `internal/api/agents.go:34`, `internal/api/server.go:139-145`, `internal/ws/handler.go:70`, `hub.go:66` | `POST /api/agents/{id}/rotate-key` (devuelve la key una sola vez) + `PUT /api/agents/{id}` para renombrar + `hub.Disconnect(agentID)` llamado desde delete y rotación; revalidar la key en el read-loop | M | ✓ |
| P2 | Cero rastro de auditoría de autenticación, y los logs de request de chi **nunca llegan al Console** | `internal/api/auth.go:34`, `internal/ws/handler.go:70-75`, chi v5.3.0 `middleware/logger.go:177` | Loguear usuario+IP+resultado en `handleLogin` y en el rechazo de `authenticateAPIKey`; reemplazar `middleware.Logger` por `RequestLogger` con formatter que escriba al logger estándar; agregar `middleware.RealIP` | S | ✓ |
| P2 | Un agente puede inyectar líneas falsas en el Console del admin vía `agent_log` multilínea | `internal/ws/handler.go:125`, `web/src/pages/Console.tsx:43` | Cortar `msg.Log` por `\n` y emitir una línea prefijada por cada una, truncando y descartando caracteres de control; ídem `msg.Error` y `msg.CurrentFile` | S | — |
| P3 | El agente no tiene frontera de confianza contra el servidor: filesystem completo legible y exfiltrable a un destino que elige el servidor | `cmd/agent/main.go:341`, `cmd/agent/internal/config.go`, `internal/backup/db.go:82` | `allowed_paths` obligatorio en `agent.yaml`, validado en `handleBrowse`, en `FilesSource.Paths` y en el conn_string de sqlite (`EvalSymlinks` + prefijo); allowlist de destinos del lado del agente | M | ✓ |

**Sobre el P3:** los hechos son ciertos pero **describe el producto funcionando como fue diseñado**. El actor requerido es el admin autenticado, dueño declarado de los hosts, cuya función legítima es elegir qué respaldar y a dónde subirlo; un file browser que arranca en `/` es feature explícita. No hay escalada: quien puede crear un job `files` con paths arbitrarios no gana nada con `browse`. Queda como defensa en profundidad, sobre el eje que la ronda 1 ya abrió con "El unit de systemd generado corre como root, sin hardening".

### L2. `data-path` — el byte desde el origen hasta el artefacto

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | `backup_runs` no registra a qué destino fue el archivo: `storage_path` guarda solo el basename y la fila no tiene `storage_destination_id` *(3 lentes)* | `migrations/001_initial_schema.up.sql:40-50`, `cmd/agent/main.go:299,329`, `internal/ws/handler.go:139-149` | Agregar `storage_destination_id` a `backup_runs` y snapshotearlo al crear el run; que `Upload` devuelva la ubicación final (`s3://bucket/prefix/key`, `sftp://host/path`) y persistirla | M | ✓ |
| P2 | Un backup de 0 archivos o 0 bytes se marca `success` sin ninguna validación | `internal/backup/files.go:32-45,82-85`, `internal/ws/handler.go:139-149` | Fallar (o marcar `warning`) si `file_count==0`/`size_bytes==0`; chequeo de desviación contra el último run exitoso (caída >90%) que dispare el notifier | S | ✓ |
| P2 | **PROFUNDIZA** un slash final en un source path aplana el árbol a la raíz del tar | `internal/backup/compress.go:54-58`, `web/src/pages/Jobs.tsx:96-100` | `filepath.Clean` sobre el root antes de calcular el prefijo + namespace determinístico por origen (índice o hash), no basename | S | ✓ |
| P2 | El dump completo de la DB queda en texto plano en `os.TempDir()` y sobrevive a SIGKILL/OOM sin que nadie lo limpie *(2 lentes)* | `cmd/agent/main.go:255-268` | `os.MkdirTemp` en directorio 0700 propio del agente (o pipear el dump directo al pipeline de compresión/cifrado sin tocar disco); barrer `nat-backup-dump-*` huérfanos al arrancar; manejar SIGTERM | M | — |
| P2 | Hardlinks se duplican y los sparse files se expanden a su tamaño aparente | `internal/backup/compress.go:45-50,64-70` | Mapa `inode → primer nombre` (`Stat_t.Nlink/Ino`) para emitir `tar.TypeLink`; detectar huecos con `SEEK_HOLE` o advertir cuando `info.Size()` supera los bloques asignados | M | — |
| P2 | La lista de source paths se separa por coma y se trimea: un path válido puede convertirse en **otro path válido** | `web/src/pages/Jobs.tsx:99` | Lista de paths (uno por fila) en vez de input de una línea; validar cada path contra el FileBrowser del agente antes de guardar | S | — |
| P3 | El material para descifrar un backup solo existe dentro del sistema que el backup protege | `internal/models/models.go:65`, `internal/api/jobs.go:59-66` | Mostrar la passphrase una vez al crear el job con checkbox de "la guardé offline", y/o "recovery kit" descargable (passphrase + parámetros KDF + formato) | M | ✓ |
| P3 | El artefacto no lleva ningún manifiesto: ni host, ni paths origen, ni fecha, ni tipo de job | `internal/backup/compress.go:37-79` | Primera entrada del tar = `MANIFEST.json` (hostname, versión del agente, job id/name/type, paths, timestamp UTC, compresión, cifrado, file_count, bytes, sha256 por archivo) | M | ✓ |
| P3 | Los dumps de DB quedan archivados bajo el nombre del temp aleatorio, sin motor ni formato | `cmd/agent/main.go:255-280`, `internal/backup/compress.go:54-58` | Nombre lógico `<job>-<dbtype>-<UTC>.sql\|.archive\|.sqlite` en el header del tar + motor y comando de restore en el MANIFEST | S | ✓ |
| P3 | **PROFUNDIZA** el algoritmo de compresión solo vive en la extensión del nombre y puede quedar vacía | `cmd/agent/main.go:299`, `internal/api/jobs.go:113-120`, `internal/backup/files.go:27-29` | Validar el enum `{zstd,gzip}` en create **y** update con el mismo default; armar el nombre con el algoritmo efectivamente usado; usar `.zst`/`.gz` | S | ✓ |

**Notas de calibración honestas.** El P3 del "material para descifrar": no devolver un secreto por la API es la conducta **correcta**, no un defecto — el campo es write-only por diseño; la premisa "solo existe dentro del sistema" es falsa porque la passphrase la eligió y tipeó el operador. Es una recomendación de proceso, no una línea de código defectuosa. El P3 del manifiesto: es defensa en profundidad sobre un flujo (restore) que **todavía no existe**. El P3 de la compresión: el núcleo del título es falso — el artefacto declara su algoritmo en los magic bytes, así que la extensión "puede mentir" es intrascendente; se solapa además con "handleUpdateJob no valida los campos obligatorios que sí valida el create" (P2, ronda 1), misma causa raíz. El "0 archivos = success": la UI **sí** muestra el `0` (`Runs.tsx:162,240`) — lo que falta es validación y alerta, no visibilidad, y si el path no existe `WalkDir` sí falla; el agujero es solo "path existe pero vacío" (mount NFS caído, volumen desmontado).

### L3. `race-repro` — carreras reproducidas con tests

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | `Register` y `Unregister` viajan por canales distintos: un disconnect rápido deja un agente **fantasma** en `h.agents` | `internal/ws/hub.go:39-40,59-83` | Eliminar los dos canales: `Register`/`Unregister` sincrónicos bajo `h.mu` para el mapa, y el UPDATE de DB en goroutine aparte. Si se conserva el event loop, **un solo canal** con comando etiquetado `{op, ac}` para que el orden sea total | M | ✓ |
| P2 | `handleConsoleWS` se suscribe **antes** de sacar el snapshot: las líneas de esa ventana llegan duplicadas al Console | `internal/api/console.go:135` vs `:140` | `subscribeWithHistory() (chan []byte, [][]byte)` bajo un único `h.mu.Lock()`, corte atómico | S | — |
| P3 | **NO REPRODUCE**: `DeliverBrowseResult` no puede bloquear el read-loop del agente (ver sección 5) | `internal/ws/hub.go:214-221,184` | Bajar el finding de la ronda 1 a P3 informativo; si se quiere blindar contra requestIDs repetidos, `select { case ch <- res: default: }` (1 línea) | S | — |

**Sobre el agente fantasma:** reproducido — `PHANTOM AGENTS: 57/200`, `76/200`, `57/200` en tres corridas (`0/50` con 2 ms de gap entre Register y Unregister, o sea la ventana es el par encolado junto). El log del propio hub lo delata: `agent agent-187 disconnected` impreso **antes** de `agent agent-187 connected`. `reconcileOffline` nunca lo repara (solo marca offline a los que la DB cree online, y el fantasma ya quedó offline por `hub.go:78`): queda DB=offline / hub=connected. **Pero el "para siempre" del título es falso**: el agente reconecta cada 10 s (`main.go:60-72`) y `handler.go:86` pisa la entrada fantasma. Solo persiste si el agente no vuelve nunca, y en ese caso el daño incremental sobre el P0 de la ronda 1 ("runs despachados a un agente offline quedan en `running` para siempre") es mínimo.

### L4. `state-machine` — transiciones ilegales e invariantes

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | `reconcileOffline` corre en el mismo goroutine que `register` y puede marcar offline a un agente que acaba de conectar | `internal/ws/hub.go:59-67,106-135`, `internal/ws/handler.go:78,86` | Invertir el orden: registrar en el hub primero y escribir `status='online'` después (o desde `Hub.Run` tras insertar en el mapa); condicionar `reconcileOffline` a `last_seen < NOW() - interval '2 minutes'` | S | ✓ |
| P2 | El cancel es solo advisory: el run pasa a `cancelled` pero la fase en vuelo (walk+compress, dump o upload) corre hasta terminar *(2 lentes)* | `cmd/agent/main.go:293-321`, `internal/backup/files.go:26`, `internal/storage/interface.go:13` | Propagar `context.Context` a `BackupFiles`, `Dump` y `Backend.Upload`, chequeándolo en el loop de copia | M | ✓ |
| P2 | Ninguna columna de estado tiene `CHECK` constraint: el enum no se sostiene en ningún lado | `migrations/001_initial_schema.up.sql:11,45` | Migración con `CHECK (status IN ('running','success','failed','cancelled'))` y `CHECK (status IN ('online','offline'))`, + constantes en `models.go` + `docs/schema.md` + `types.ts` en el mismo cambio | S | — |
| P3 | **PROFUNDIZA** un `offline` erróneo es permanente: nada vuelve a escribir `status='online'` salvo el handshake | `internal/ws/handler.go:129-134`, `internal/ws/hub.go:110,130` | Que el heartbeat ejecute `UPDATE agents SET status='online', last_seen=NOW()` y que `reconcileOffline` sea bidireccional | S | ✓ |
| P3 | `AgentMessage.Status` es un campo del protocolo que el agente escribe y el server nunca lee | `internal/ws/types.go:41`, `cmd/agent/main.go:326` | Borrar el campo y su envío; el tipo de mensaje es la única fuente del estado terminal | S | — |

**Impacto real de los dos primeros.** El de `reconcileOffline`: **ninguna decisión funcional del server lee `agents.status`** — `agents.go:95,126` gatean con `s.hub.IsConnected()` (mapa en memoria) y `DispatchJob` usa el mismo mapa. El efecto es cosmético/UI (`Dashboard.tsx:39`, `Agents.tsx:229`, `Jobs.tsx:355` deshabilita el file browser). El del cancel: los checkpoints de `main.go:238,250,270,293` abortan **antes** del `Upload`, así que en el caso común no queda artefacto huérfano; el desperdicio de CPU/RAM/red hasta terminar la unidad en curso sí es real, y el artefacto huérfano solo aparece si el cancel llega ya dentro de `Upload()`.

### L5. `failure-injection` — DB, red, disco y proceso cayéndose

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | El scheduler se queda vacío en silencio si el `SELECT` de `loadJobs` falla al arrancar; y si el INSERT del run falla, la ventana de backup se pierde sin dejar rastro | `internal/scheduler/scheduler.go:51-63,39-45,102-109` | `loadJobs` devuelve error y reintenta con backoff (o aborta el arranque); registrar un `missed_run` cuando `triggerJob` no puede crear la fila, y reintentar el INSERT | M | ✓ |
| P2 | SFTP: el campo `Timeout` es código muerto — el handshake SSH puede colgarse para siempre con el backup entero en RAM | `internal/storage/sftp.go:43,46,51` | `net.DialTimeout` + `conn.SetDeadline(30s)` antes de `ssh.NewClientConn`, limpiado tras el handshake; deadline global de transferencia | S | ✓ |
| P2 | El fallo del canal de alertas es invisible, y hay un modo de fallo **determinista** (`TLS=false` + `username` contra relay sin STARTTLS) | `internal/ws/handler.go:194-196`, `internal/notify/email.go:48-54` | Persistir el resultado del envío y exponerlo en la UI; reintentar con backoff; validar la config al guardarla rechazando auth sin TLS/STARTTLS | M | ✓ |
| P3 | `/healthz` es una constante: un servidor con la DB muerta se reporta sano para siempre | `internal/api/server.go:119-121` | `/healthz` liveness puro + `/readyz` con `db.PingContext` y timeout corto, 503 ante fallo | S | ✓ |
| P3 | Dos instancias de server contra la misma DB: backups duplicados, runs fantasma y todos los agentes marcados offline | `internal/scheduler/scheduler.go:39-45`, `internal/ws/hub.go:56-62,106-135`, `cmd/server/main.go:60-62` | `pg_try_advisory_lock` al arrancar scheduler y reconciliador (solo el líder programa), o estado online/offline por heartbeat con TTL en vez de flag global | M | ✓ |

**Calibraciones.** El del scheduler: la premisa de arranque está bloqueada aguas arriba — `cmd/server/main.go:39-46` hace `RunMigrations` y `Connect` (con `Ping`) y ambos usan `log.Fatalf`, así que con Postgres caído el proceso **no arranca**; la ventana es un blip transitorio muy angosto. Y "no hay ningún camino de recarga" es **falso**: `jobs.go:85,135,197` llaman `sched.AddJob` en create/update/toggle. El de `/healthz`: no tiene consumidor en este repo — no hay `Dockerfile`, `docker-compose.yml` solo define `postgres`, no hay manifiestos k8s ni LB. El de dos instancias: el proyecto **no tiene ninguna forma de despliegue multi-instancia**, y dos de sus tres daños ya son findings de la ronda 1. El de las alertas: "nadie se entera nunca de que los backups fallan" es falso — el run queda en `failed` con `error_message` y la UI lo muestra; el email es canal secundario. Lo genuinamente nuevo es el **modo de fallo determinista de SMTP**.

### L6. `tests-lie` — qué mentira cuenta la suite

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | `make test` — el comando documentado — está **rojo** en cualquier máquina sin Postgres | `internal/db/db_test.go:20-22`, `internal/db/migrate_test.go:12-14`, `Makefile:26-27` | Gate por `TEST_DATABASE_URL` presente en vez de `testing.Short()`, para que el default sea verde y la integración sea opt-in; o `test` depende de `dev-up` | S | ✓ |
| P2 | `make test` corre migraciones contra la base que tengas en `DATABASE_URL` — incluida la de producción | `internal/db/db_test.go:12-17`, `internal/db/migrate_test.go:15-35` | Variable separada `TEST_DATABASE_URL` (nunca `DATABASE_URL`) + abortar si el DSN no apunta a localhost o la base no termina en `_test` | S | ✓ |
| P2 | Ningún test lee jamás un archivo producido: los tests llamados `RoundTrip` nunca hacen round trip | `internal/backup/compress_test.go:15-49` | Helper `untar(t, buf) map[string][]byte` y afirmar set exacto de paths + contenidos; fixture con symlink, FIFO, dos dirs con el mismo basename y un archivo que crece | M | ✓ |
| P2 | Los 3 tests de cifrado usan entradas menores a un chunk: el loop multi-chunk nunca se ejecutó | `internal/backup/encrypt_test.go:12-44` vs `encrypt.go:17` (64 KB) | Round-trip de ≥256 KB + casos explícitos: truncar el último chunk, intercambiar dos chunks, duplicar uno (los tres deben dar error) | S | ✓ |
| P2 | Los handlers están atados a `*sqlx.DB` concreto: ningún handler es testeable sin un Postgres vivo, y no hay arnés para levantarlo | `internal/api/server.go:42`, `internal/api/jobs.go:32,69,112,134,149` | Interfaces angostas por recurso (`JobStore`, `AgentStore`, `StorageStore`) o `testcontainers-go` con `TestMain` de paquete. **Sin uno de los dos, cualquier propuesta de "agregar tests de handlers" es inaplicable** | L | — |
| P2 | Seis tests no ejercitan ninguna lógica: `NotNil` sobre constructores que no pueden devolver nil, y constantes comparadas contra sus propios literales | `internal/backup/db_test.go:27-33`, `notify/email_test.go:12-21`, `notify/retention_test.go:11-14`, `storage/local_test.go:29-35`, `models/models_test.go:41-54`, `ws/types_test.go:40-47` | Borrarlos o darles contenido: `TestDBDumper_Types` debería afirmar el comando y los argumentos por motor — donde vive el bug de `mysqldump` | S | — |
| P2 | El único mock del repo no registra nada y nunca se afirma: `AddJob`/`RemoveJob`/`triggerJob` no se ejecutan en ningún test | `internal/scheduler/scheduler_test.go:14-23` | Mock que acumule llamadas con mutex; tests de `AddJob`/`RemoveJob`/`UpdateJob` que afirmen `len(s.cron.Entries())` y mapa vacío tras remove; trigger manual para el dispatch | M | — |
| P2 | **PROFUNDIZA** el test de `cmd/agent` vive en el paquete equivocado: reporta `ok, coverage 0.0%` y esconde que `cmd/agent/internal` no tiene tests | `cmd/agent/agent_test.go:1-21` | Mover a `cmd/agent/internal`; extraer de `main.go` el switch de ejecución de jobs y el envío de `job_done`/`job_failed` para que sean testeables sin proceso ni WebSocket | M | — |
| P2 | **PROFUNDIZA** el mismatch de JSON keys de S3/SFTP es un contrato entre dos lenguajes y **ningún test de ninguno de los dos lados puede verlo** | `internal/storage/local_test.go:29-39` | Fixtures golden en `testdata/` (`s3.json`, `sftp.json`) compartidos: un test Go que las decodifique y afirme que todos los campos quedan poblados (hoy falla), y un test de la UI que afirme que el body serializado es igual al fixture | M | — |
| P3 | Los tests de config leen el entorno del proceso con `os.Setenv` en vez de `t.Setenv`: fallan según la shell del dev | `internal/config/config_test.go:13-41` | `t.Setenv` (restaura solo y falla si el test es paralelo); limpiar `PORT`/`TLS_*` al inicio de cada caso | S | — |

**Contexto que la lente aportó y conviene no perder.** `grep -rn "tar.NewReader\|zstd.NewReader\|gzip.NewReader"` devuelve **cero hits en todo el repo** — ni en tests ni en producción; nada, en ningún lado, lee un archivo que este sistema haya producido. `n` en `TestTarCompress_FileCount` es `totalBytes`, no un conteo de archivos: ese test **no podría** verificar el file count aunque quisiera. Y sobre el multi-chunk: la lente ejecutó el camino no cubierto (65535 / 65536 / 65537 / 200000 bytes) y **no esconde ningún defecto** — es un hueco de cobertura sin bug latente; lo que sí demostró es truncado silencioso (cortar a 2 de 4 chunks → `err=<nil>`), que ya es el P3 de la ronda 1 sobre truncado/reordenamiento.
Del lado positivo: cero `t.Skip` incondicionales, cero subtests declarados que no corran, cero estado compartido entre tests, higiene de filesystem impecable (todos usan `t.TempDir`), ningún test depende del orden.

### L7. `build-embed-release` — del commit al binario en el host

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | El badge de versión compara con y sin prefijo `v`: todo agente actualizado muestra "update available" para siempre, y renderiza `vv0.1.0` | `web/src/pages/Agents.tsx:80,213,225`, `.github/workflows/release.yml:35` | Normalizar en un solo lugar (strippear la `v` también de `agent.version`, o de ninguno); comparar con semver ordenado, no con `!==` | S | ✓ |
| P2 | Los binarios linux/amd64 del release salen dinámicamente linkeados contra la glibc del runner; arm64 y windows salen estáticos | `.github/workflows/release.yml:32-44` | `CGO_ENABLED=0` en los cuatro pasos de build (ninguna dependencia lo necesita: `lib/pq` es Go puro y sqlite se invoca por exec) | S | ✓ |
| P2 | La UI embebida se sirve sin ningún validador de caché y sin compresión: ~1,05 MB en crudo en cada carga | `internal/api/server.go:87-108,113-117` | `middleware.Compress(5)` + wrapper que ponga `Cache-Control: public, max-age=31536000, immutable` en `/assets/*` (ya llevan hash de contenido) y `no-cache` en `index.html` | S | ✓ |
| P2 | El self-update no compara versiones: descarga siempre y puede **degradar** el agente a una versión anterior | `cmd/agent/update.go:18-27`, `web/src/pages/Agents.tsx:238` | Consultar `/releases/latest` (o recibir la versión objetivo en el mensaje WS), comparar con semver y salir sin tocar nada si la corriente es ≥ la remota, devolviendo un motivo al server | M | — |
| P2 | Todo binario publicado queda estampado como build de árbol sucio (`vcs.modified=true`) y con las rutas absolutas del runner (sin `-trimpath`) | `.github/workflows/release.yml:29-37` | `-trimpath` en los cuatro `go build` y en el Makefile; sacar `internal/api/static/dist` del control de versiones (o `-buildvcs=false` explícito) | S | — |
| P2 | El fallback SPA devuelve 200 con `text/html` para cualquier `/assets/*` inexistente en lugar de 404 | `internal/api/server.go:99-106` | Excluir del fallback los prefijos de assets y extensiones conocidas → 404 real; reservar `index.html` solo para rutas de navegación | S | — |
| P2 | **PROFUNDIZA** `bin/` está en `.gitignore` pero los binarios están trackeados, así que la regla no aplica y cada release los vuelve a commitear | `.gitignore:2-3`, `bin/*`, `.claude/commands/release.md:85-88` | `git rm --cached bin/nat-backup-agent bin/nat-backup-agent.exe bin/nat-backup-server`; cambiar el paso 5 del release para que no use `git add -A` | S | — |
| P2 | El versionado no puede expresar incompatibilidad: el procedimiento mapea breaking → minor y el protocolo WS no negocia versión | `.claude/commands/release.md:35,61-66`, `internal/ws/handler.go:80` | Corregir el mapeo a semver (breaking → major salvo excepción 0.x declarada) + `protocol_version` entero en el `hello` que el server valide, rechazando el handshake con motivo legible | M | — |
| P3 | Los binarios se publican sin strippear: ~16 MB de agente con DWARF completo que el self-update baja entero en cada update | `.github/workflows/release.yml:34-58`, `Makefile:6` | `-ldflags "-s -w -X main.version=..."` en workflow y Makefile (~30% menos, sin perder nombres de función en los stack traces) | S | — |

**Calibraciones.** Del cgo: el impacto declarado estaba mal — `objdump -T` sobre el binario amd64 da como máximo `GLIBC_2.34`, así que Debian 12 (2.36) y Ubuntu 22.04 (2.35) **arrancan sin problema**; solo revientan glibc < 2.34 (RHEL 8 / Debian 11 / Ubuntu 20.04). Fix de una línea, alcance limitado a distros EOL o de soporte extendido. Del caché: es puramente performance sobre un panel self-hosted normalmente en LAN, y 1,05 MB es el techo (las woff2 tienen `unicode-range`). Del badge de versión: 100% reproducible pero de presentación; el peor efecto es que el usuario dispare un `selfUpdate()` innecesario, que re-descarga y re-execea el mismo binario. El del `bin/` trackeado: ya pasó — el commit `4de649c` incluye `bin/nat-backup-server | Bin 13535980 -> 13535980 bytes`; van 18 revisiones de blob binario en 13 commits y `.git` pesa 168 MB. Y el binario que queda en el repo tiene `version="dev"` y `vcs.modified=true`: **no es lo que se publica**.

### L8. `operator-ux` — el día a día de quien opera esto

| Sev | Título | Location | Fix | Effort | V |
|---|---|---|---|---:|:-:|
| P2 | La duración de un run no se calcula ni se muestra en ningún lado | `web/src/pages/Runs.tsx:160-171,237-241` | `finished_at - started_at` en fila y drawer; tiempo transcurrido en vivo para runs en curso; comparar contra la mediana de los últimos N runs del mismo job | S | — |
| P2 | El Dashboard no dice **qué** falló: "Recent Runs" muestra un id truncado y nada más | `web/src/pages/Dashboard.tsx:149-162` | Resolver job/agente en la fila (o embeberlos en `GET /api/runs`), mostrar `error_message` truncado en los fallidos y linkear al drawer de logs | S | — |
| P2 | Cero autoría y cero auditoría: ninguna acción destructiva queda atribuida a nadie | `migrations/001_initial_schema.up.sql:23-63`, `internal/api/auth.go:39-43` | Registrar el `sub` del JWT en una tabla `audit_log` (actor, acción, recurso, timestamp, payload) desde un middleware sobre las rutas mutantes; después, usuarios múltiples con rol viewer/admin | M | — |
| P2 | No existe pausa global ni modo mantenimiento: el único control es el toggle por job | `web/src/pages/Jobs.tsx:271-277`, `internal/api/server.go:152-157` | Flag global de pausa (`system_settings` + chequeo en `scheduler.triggerJob`) con banner visible, y un `enabled` a nivel agente que el scheduler respete | M | — |
| P2 | La página Jobs no tiene búsqueda, filtro, orden ni paginación, y `GET /api/jobs` devuelve todo (incluido el `source` JSONB completo) | `web/src/pages/Jobs.tsx:257-309`, `internal/api/jobs.go:30-41` | Paginar y filtrar server-side (agent_id, type, enabled, nombre) + buscador en la página; devolver `job_name`/`agent_name` embebidos en `GET /api/runs` en vez de bajar el catálogo entero | M | — |
| P2 | **PROFUNDIZA** retirar un destino de storage es imposible sin editar job por job — y editar un job **descifra sus backups** | `web/src/pages/Storage.tsx:247`, `migrations/001_initial_schema.up.sql:30`, `web/src/pages/Jobs.tsx:204` | 409 con el conteo y la lista de jobs afectados + reasignación masiva desde ese error; y que el update de job **preserve** la passphrase cuando el campo llega vacío | M | — |
| P2 | **PROFUNDIZA** borrar un agente deja su proceso golpeando el servidor cada 10 s y quemando bcrypt contra toda la tabla | `web/src/pages/Agents.tsx:386`, `cmd/agent/main.go:60-72`, `internal/ws/handler.go:203-217` | Corregir el texto del diálogo; backoff exponencial con tope en el agente ante un close 4003; cachear/indexar la auth de API keys en vez de escanear | M | — |
| P3 | Primer arranque sin guía: el Dashboard vacío no dice qué hacer y el modal de job no deja crear lo que falta | `web/src/pages/Dashboard.tsx:68-98`, `web/src/pages/Jobs.tsx:359-371` | Empty state accionable con checklist de 4 pasos y links; en los Selects vacíos, "No hay destinos — crear uno" con navegación a `/storage` | S | — |

**El más importante de este grupo es el `PROFUNDIZA` del storage destination**, porque **encadena con el P0 de la ronda 1**: para dar de baja un bucket hay que reasignar cada job desde el modal de edición, y ese modal manda `encrypt_passphrase: ''` (`Jobs.tsx:204`) — o sea, **retirar un destino de storage borra la passphrase de todos los jobs cifrados en una sola sesión de trabajo**, y a partir de ahí los backups salen en claro con extensión `.enc`. Es el disparador masivo del P0 "Editar un job cifrado borra la passphrase". Si se arregla una sola cosa de esta ronda además de los P1, que sea esta cadena.

---

## 5. Correcciones a la ronda 1

Tres recalibraciones. Las tres cambian conclusiones del informe anterior.

### 5.1 `DeliverBrowseResult` **no** puede bloquear el read-loop de un agente — el P1 de la ronda 1 está mal calibrado

La ronda 1 afirma: *"[P1] `DeliverBrowseResult` puede bloquear el read-loop de un agente para siempre @ `internal/ws/hub.go:214-221`"*, con la consecuencia de que el agente deja de procesar `job_done`/`job_failed`/heartbeat.

El canal se crea con **buffer 1** (`hub.go:184`) y hay exactamente **una respuesta por requestID** (`uuid.New()` en `agents.go:100`, un solo `writeJSON` en el agente en `main.go:356`), así que el `ch <- ...` no puede bloquear. La lente `race-repro` probó tres escenarios bajo `-race`: (a) delivery sin waiter registrado → retorna al instante (`ok==false`); (b) delivery normal con waiter → retorna; (c) delivery **duplicado** para el mismo requestID → `both deliveries returned; no permanent block`. Para bloquear harían falta 2+ respuestas con el mismo requestID **y** que la primera siguiera en el buffer — combinación inalcanzable con el código actual.

**Acción:** bajar ese finding a **P3 informativo**. El `select { case ch <- res: default: }` de una línea sigue valiendo como blindaje contra un agente malicioso, pero no es un P1.

**Caveat:** este finding específico **no pasó el pase adversarial de esta ronda** (la lente reporta haberlo reproducido bajo `-race`, pero el verificador no lo re-corrió). Confirmalo antes de cerrar el P1 de la ronda 1.

### 5.2 El JWT del query string **no** se re-emite por el Console — la mitad del P0 de la ronda 1 es falsa

La ronda 1 afirma: *"[P0] El JWT viaja en query string y termina escrito en los logs y re-emitido por el Console"*, razonando que `middleware.Logger` imprime `r.RequestURI` y que ese log va por `io.MultiWriter` al `ConsoleHub`, quedando en el ring buffer de 500 líneas y re-emitiéndose a cualquier suscriptor.

Verificado en el módulo real: `~/go/pkg/mod/github.com/go-chi/chi/v5@v5.3.0/middleware/logger.go:177` →

```go
DefaultLogger = RequestLogger(&DefaultLogFormatter{Logger: log.New(os.Stdout, "", log.LstdFlags), NoColor: !color})
```

Es una **instancia propia** de `*log.Logger`. `cmd/server/main.go:66` (`log.SetOutput(io.MultiWriter(os.Stderr, srv.GetConsoleHub()))`) solo cambia el logger **estándar**, así que **ninguna línea de request de chi llega al ConsoleHub**.

**Consecuencia doble:** (a) el P0 sigue siendo real pero más chico — el token queda en `stdout` y en access logs de proxies, **no** en el ring buffer ni re-emitido a suscriptores del Console; (b) aparece un problema nuevo que la ronda 1 no vio: **el Console, única superficie de observabilidad del producto, no muestra ni una sola línea de request HTTP**. Ver el P2 `attacker` correspondiente.

### 5.3 `CheckOrigin` siempre `true` (P2 de la ronda 1) no habilita ninguna cadena

La lente `attacker` buscó explícitamente el CSWSH: los tres upgraders tienen `CheckOrigin` que devuelve `true`, pero **la app no usa cookies** — el JWT vive en `localStorage` y viaja por query param o header, así que una conexión WebSocket cross-site no lleva credenciales. Sigue siendo defensa en profundidad legítima, pero **conviene despriorizarlo** frente a los findings de esta ronda.

### 5.4 Nota menor: la hipótesis del `dist` desactualizado es falsa

No es un finding de la ronda 1, pero cierra un riesgo que quedó insinuado en *"Nada verifica que `internal/api/static/dist` corresponda al `web/src` actual"*. El `dist` commiteado **no está viejo**: `index-wUiSt3fK.js` contiene el string `192.168.1.100`, que solo aparece en el diff de `Agents.tsx` del último commit `4de649c`. Además el release corre `make build-ui` como primer paso, así que el binario publicado nunca usa el `dist` commiteado. **El riesgo real es el opuesto y ya está reportado** ("npm install sin lockfile"): la UI publicada se resuelve desde rangos `^` en CI y no es la que nadie probó localmente.

---

## 6. Vista consolidada de las dos rondas — top 10 por riesgo real

Si se lee una sola sección de los dos informes, que sea esta. Ordenado por riesgo real, no por severidad nominal.

| # | Qué | Dónde | Ronda |
|---:|---|---|---|
| **1** | **`.env` con secretos reales commiteado a un repo público.** `JWT_SECRET`, `ENCRYPTION_KEY` (la clave AES que protege *todas* las credenciales de storage y *todas* las passphrases), `ADMIN_PASSWORD=[REDACTED_ADMIN_PASSWORD]` y el `DATABASE_URL`, en claro y alcanzables en cualquier clon o fork. **Es un compromiso ya ocurrido, no un riesgo.** | blob `e19de21` en `528a1bc` | R1 P0 |
| **2** | **Editar un job cifrado borra la passphrase → los backups siguientes salen en claro con extensión `.enc`, y los anteriores quedan irrecuperables.** Agravado por dos hallazgos nuevos: el único test que guarda esa propiedad **no puede fallar** (R2 P1), y **retirar un destino de storage obliga a editar todos los jobs**, disparando el bug en masa (R2 P2). | `Jobs.tsx:204` → `jobs.go:104,117` → `files.go:50` → `main.go:301` | R1 P0 + R2 |
| **3** | **El producto no puede restaurar.** `storage.Backend` solo declara `Upload`; `DecryptStream` no tiene más caller que un test. R2 agrega: el formato es reconstruible pero **solo leyendo el código fuente de esta versión exacta** (§1.1), el artefacto no lleva manifiesto y no hay export de la passphrase. | `internal/storage/interface.go:11-14` | R1 P1 + R2 |
| **4** | **`panic: send on closed channel`, ahora con una vía fatal.** El bug de fondo es de R1; R2 mostró que desde el read-loop del agente lo atrapa chi y desde el **scheduler mata el proceso**, porque `cron.New()` no instala `cron.Recover`. Sin supervisor para el server, es outage permanente. | `hub.go:73` vs `168/197/233/255` + `scheduler.go:31` | R1 P0 + **R2 P1** |
| **5** | **Los backups de bases de datos están rotos o silenciosamente corruptos.** `mysqldump` recibe el DSN como nombre posicional de base (se conecta a localhost y busca una base llamada así); el "dump" de SQLite es una copia cruda sin lock que pierde el WAL. Ambos se reportan `success`. | `internal/backup/db.go:60-61,80-92` | R1 P1 ×2 |
| **6** | **Dos de los tres backends de storage están rotos de fábrica** por mismatch de json tags entre la UI y los structs Go: S3 queda con credenciales vacías, SFTP falla el unmarshal. **Solo `local` funciona.** R2 agrega que **ningún test de ninguno de los dos lados puede detectarlo** (es un contrato entre dos lenguajes sin artefacto compartido). | `Storage.tsx:48-71` vs `models.go:109-124` | R1 P0 + R2 P2 |
| **7** | **Los runs quedan colgados en `running` para siempre** — cinco causas, ningún reaper. R2 agrega una sexta vía (el agente fantasma en `h.agents` por inversión register/unregister) y muestra que el **cancel tampoco cierra el estado**: responde 204 sin entrega y `job_done` resucita el run a `success` (R2 P1). | `hub.go:144-172`, `runs.go:35-54`, `handler.go:139-150` | R1 P0 + **R2 P1** |
| **8** | **La retención borra la fila de la DB y nunca el archivo del storage**, corriendo todos los días a medianoche. R2 explica por qué es irreparable con el esquema actual: `storage_path` guarda solo el basename y `backup_runs` no tiene `storage_destination_id`, así que ni con la fila se puede resolver el destino histórico. | `retention.go:46-53` + `001_initial_schema.up.sql:40-50` | R1 P1 + R2 P2 |
| **9** | **El agente es frágil de las dos puntas:** bufferiza el backup completo en RAM antes de subir (OOM garantizado a escala) y **no pone write deadline en el WS**, de modo que un servidor lento lo congela entero por el mutex del paquete `log` (R2 P1, reproducido). Y tras un OOM-kill queda un **dump completo de la base en claro en `/tmp`** que nadie limpia. | `main.go:318` · `main.go:84-91,126` · `main.go:255-268` | R1 P0 + **R2 P1** + R2 P2 |
| **10** | **Ningún fix de esta lista va a llegar a los hosts.** Los instaladores reemplazan el binario y **nunca reinician el servicio**: en Linux systemd sigue con el inode viejo mientras el script dice "instalado correctamente"; en Windows el script muere a mitad. | `install.sh:72-74`, `install-agent.ps1:56-58` | **R2 P1** |

Quedaron fuera del top 10 por poco: SFTP con `InsecureIgnoreHostKey` (MITM en cada upload, R1 P1), passphrase y credenciales de storage viajando en claro por `ws://` con la config por defecto (R1 P1), credenciales SMTP en claro devueltas al browser (R1 P0), y el diálogo de borrar job que promete conservar el historial mientras la FK lo cascadea (R2 P1).

---

## 7. Qué quedó sin cubrir después de dos rondas

Dos rondas, 15 lentes/capas, ~244 findings únicos — y aun así hay dimensiones enteras **sin tocar**, todas por la misma razón: **nada se ejecutó contra infraestructura real**.

**1. Ningún camino que toque Postgres fue ejercitado.** Las carreras del hub se probaron con `db=nil`; las cascadas de FK, el estado `dirty` de golang-migrate, los planes de query, el comportamiento de la retención al borrar y el efecto real de `reconcileOffline` se dedujeron leyendo SQL. La duración real del stall del event loop de `Hub.Run` — el multiplicador principal del agente fantasma — **no se midió**.
→ *Hace falta:* `testcontainers-go` con `TestMain` a nivel de paquete que levante Postgres una vez y corra las migraciones. Sin eso, el P2 `tests-lie` sobre handlers atados a `*sqlx.DB` bloquea cualquier plan de "agregar tests de handlers".

**2. Nadie descifró nunca un artefacto real producido por el agente.** El formato de §1.1 se reconstruyó **leyendo el código**, no leyendo un archivo. El P1 "no existe restauración" sigue sin prueba de que lo que se sube sea recuperable.
→ *Hace falta:* un test end-to-end `BackupFiles → Upload(local) → leer del disco → DecryptStream → untar → comparar árbol byte a byte`. Es el test más valioso que le falta a este repo y no requiere infraestructura.

**3. Cero contacto con S3, SFTP o Windows reales.** El 403 de S3 por credenciales vacías, el cuelgue del handshake SSH, el retryer de aws-sdk ante un `SlowDown`, la ACL del `agent.yaml` en `%ProgramData%` y el `EWINDOWS` del self-update están todos **razonados sobre código y documentación**.
→ *Hace falta:* MinIO + un contenedor `sshd` en el compose de dev, y un runner Windows en CI.

**4. Sin instancia corriendo, no hubo pentest.** Fuerza bruta contra `/api/auth/login`, flood de WebSocket sin `SetReadLimit`, DoS por el escaneo bcrypt de toda la tabla `agents`, agotamiento de disco vía `/api/upload`: todos reportados por lectura, ninguno ejecutado. Tampoco se corrió `govulncheck` sobre el árbol de dependencias.
→ *Hace falta:* una instancia de staging y una pasada de `govulncheck` en CI.

**5. La SPA no tiene tests ni se ejecutó en un browser.** Todos los findings de UI (salvo el badge de versión, que es comparación de strings) son lectura de código. Ninguno de los flujos que este informe llama "mentirosos" se vio mentir.
→ *Hace falta:* Vitest + Testing Library, empezando por los payload builders de `Storage.tsx` y `Jobs.tsx`, que es donde viven dos P0.

**6. Nada se probó a escala.** 500 jobs, 50 agentes, 100k runs: los findings de paginación y payloads son estructurales, no medidos. La latencia de reconexión creciendo linealmente con la cantidad de agentes (bcrypt scan) nunca se cronometró.

**7. Y lo que sostiene todo lo anterior: no hay CI.** El único workflow se dispara con tags `v*` y va de checkout a publicar binarios sin correr `go test`, `go vet`, `gofmt` ni `npm run lint`. Hoy `gofmt -l` reporta 4 archivos y eslint 11 errores sin que nada falle. **Cualquier fix de este informe puede revertirse mañana sin que nadie se entere.** Antes de arreglar el #2 de la lista consolidada conviene tener un `go test ./... -race` corriendo en cada push — y para eso hay que arreglar primero que `make test` esté rojo por default en cualquier máquina sin Postgres.

---

*Informe generado por review automatizado con 8 lentes transversales + verificación adversarial + consolidación. Los P1 pasaron verificación; los P2/P3 marcados `—` no. Confirmá contra el código antes de tocar nada.*
