## Release: Bump de versión y release a producción

### Contexto
Hay cambios listos en `main` para publicar como release.
El objetivo es: determinar el tipo de bump (patch/minor/major), calcular la versión nueva, commitear cambios pendientes, taggear y pushear. El tag `v*` dispara GitHub Actions que compila los 4 binarios y publica la GitHub Release automáticamente.

---

### Pasos a ejecutar en orden

#### 1. Verificar estado del repo
```bash
git status
git branch
```
Confirmá que estás en `main` y que los cambios a incluir están presentes. Si hay uncommitted changes que NO van en este release, detenete y avisame.

---

#### 2. Leer la versión actual
```bash
git describe --tags --abbrev=0 2>/dev/null || echo "sin tags"
```
Guardá ese valor como `VERSION_ACTUAL` (formato `vX.Y.Z`). Si no hay tags, la versión actual es `v0.0.0` y la primera release será `v0.1.0`.

---

#### 3. Determinar el tipo de bump

Analizá los commits desde el último tag:
```bash
git log $(git describe --tags --abbrev=0 2>/dev/null || echo "")..HEAD --oneline
```

**Señales de cambio NO retrocompatible** (cualquiera de estas → minor bump):

| Señal | Qué buscar |
|-------|-----------|
| Migraciones nuevas de DB | Archivos nuevos en `migrations/` |
| Cambio en mensajes WS entre server y agent | `internal/ws/types.go` con campos/tipos modificados o eliminados |
| Cambio en protocolo agente (`agent.yaml`) | `cmd/agent/internal/` con campos nuevos requeridos |
| Endpoints eliminados o renombrados | `internal/api/server.go` rutas quitadas |
| Cambio en modelos que rompe la API | `internal/models/models.go` campos eliminados o tipos cambiados |

```bash
# Migraciones nuevas
git diff $(git describe --tags --abbrev=0 2>/dev/null || echo "HEAD~10")..HEAD --name-only | grep 'migrations/'

# Protocolo WS
git diff $(git describe --tags --abbrev=0 2>/dev/null || echo "HEAD~10")..HEAD --name-only | grep 'internal/ws/types.go'

# Modelos
git diff $(git describe --tags --abbrev=0 2>/dev/null || echo "HEAD~10")..HEAD --name-only | grep 'internal/models/'
```

Para archivos críticos encontrados, revisá el diff puntual:
```bash
git diff $(git describe --tags --abbrev=0 2>/dev/null || echo "HEAD~10")..HEAD -- <archivo>
```

**Decisión:**
- Migraciones nuevas → **NO retrocompatible** → **minor bump**
- Cambios en `ws/types.go` que eliminan/renombran campos → **NO retrocompatible** → **minor bump**
- Cambios en `models.go` que eliminan campos de la API JSON → **NO retrocompatible** → **minor bump**
- Todo lo anterior solo agrega (nuevos endpoints, nuevos campos opcionales, nuevas rutas) → **retrocompatible** → **patch bump**
- Reescritura arquitectural significativa → **major bump**

---

#### 4. Calcular la versión nueva

Dado `VERSION_ACTUAL = vX.Y.Z`:

- **patch** → `vX.Y.Z+1`
- **minor** → `vX.Y+1.0`
- **major** → `vX+1.0.0`

Guardá como `VERSION_NUEVA`.

---

#### 5. Commitear cambios pendientes (si los hay)

Si `git status` muestra cambios sin commitear que van en este release:
```bash
git add -A
git commit -m "chore: prepare release $VERSION_NUEVA"
```

Si no hay nada pendiente, saltá este paso.

---

#### 6. Taggear y pushear

```bash
git tag "$VERSION_NUEVA"
git push origin main
git push origin "$VERSION_NUEVA"
```

Esto dispara el workflow `.github/workflows/release.yml` que:
- Compila `nat-backup-server` (linux/amd64)
- Compila `nat-backup-agent` (linux/amd64, linux/arm64, windows/amd64)
- Publica la GitHub Release con los 4 binarios + los scripts de instalación
- Genera release notes automáticas a partir de los commits desde el tag anterior

---

#### 7. Verificar el release

```bash
gh release view "$VERSION_NUEVA"
```

Confirmá que los assets están presentes (4 binarios + 2 scripts). Si el workflow de Actions todavía está corriendo, esperá y revisá con:
```bash
gh run list --limit 3
```

---

### Manejo de errores

- Si **cualquier paso falla**, detenete inmediatamente.
- Mostrá el error completo y tu hipótesis sobre la causa.
- **No intentes corregir nada solo** — preguntame cómo continuar.
- Si el tag ya fue pusheado pero el workflow falló: corregir el problema, borrar el tag local y remoto (`git tag -d vX.Y.Z && git push origin :refs/tags/vX.Y.Z`) y volver al paso 6.
