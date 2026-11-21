# GooglePhotosFS

Unidad virtual de Google Photos para Windows 10 u 11 x64. El ejecutable monta un disco local mediante la API nativa de WinFsp y se comunica directamente con la API móvil de Google Photos. El código fuente incluye el catálogo, el cliente móvil y la interfaz de montaje.

## Instalación

1. Instalá **WinFsp Core** desde su instalador oficial. [WinFsp Releases](https://github.com/winfsp/winfsp/releases/latest)
2. Extraé el paquete Windows x64 y abrí PowerShell en la carpeta del ejecutable.
3. Ejecutá estos comandos.

   ```powershell
   .\googlephotosfs.exe config
   .\googlephotosfs.exe mount
   ```

4. Abrí la unidad **P:** en el Explorador. Para desmontarla, ejecutá `.\googlephotosfs.exe unmount`.

Podés indicar otra letra con `mount R:` y `unmount R:`. La primera indexación puede tardar según el tamaño de la biblioteca. El proceso de montaje queda en segundo plano y registra errores en `mount-P.log` junto al archivo de configuración.

### Autenticación

En Windows, elegí **Manual**, iniciá sesión en EmbeddedSetup y aceptá. En las herramientas de desarrollo del navegador, buscá **Application/Storage → Cookies → accounts.google.com → oauth_token**. Pegá el valor en la consola y elegí el modo de listado. El comando verifica la credencial con Google antes de guardarla. [Google EmbeddedSetup](https://accounts.google.com/EmbeddedSetup)

`config existing` acepta el `auth_data` del backend, codificado como formulario con `androidId`, `Token` y los demás campos. `config auto` puede completar la configuración en Linux con Chrome, Chromium o Brave. Usa un perfil temporal propio y se limita a la cookie de EmbeddedSetup. Para usar ese método, compilá en Linux con `go build -o googlephotosfs ./src` y ejecutá `./googlephotosfs config auto`. Después transferí el TOML de manera privada a Windows. `GP_AUTH_DATA` permite pasar la credencial al proceso desde su entorno.

La API móvil es interna. Google puede rechazar el inicio de sesión, cambiar los endpoints o aplicar sus restricciones de cuenta. El perfil Pixel XL se habilita solo por elección explícita. La cuota que aplica Google depende del servidor y de la cuenta. Tratá `auth_data` como una credencial privada.

## Configuración

`googlephotosfs config path` imprime la ruta del TOML. En Windows está por defecto en `%APPDATA%\GooglePhotosFS`. Desmontá la unidad antes de editar la configuración. `GPHOTOSFS_CONFIG` permite indicar otro archivo.

| Clave | Valor inicial | Uso |
| --- | --- | --- |
| `filesystem.mount_point` | `P:` | Letra libre de la unidad. |
| `filesystem.root` | Vacío | Subárbol de la biblioteca que se expone como raíz. |
| `filesystem.read_only` | `false` | Limita el volumen y los comandos a lectura. |
| `google_photos.auth_data` | Vacío | Credencial móvil, guardada por `config`. |
| `google_photos.cache_db_path` | Vacío | Índice SQLite. Por defecto, un archivo por cuenta junto al TOML. Usá una copia si importás un índice existente del backend. |
| `google_photos.device_model` | `Pixel 9a` | Modelo declarado a Google. |
| `google_photos.device_make` | `Google` | Fabricante declarado al confirmar la carga. |
| `google_photos.unlimited_upload` | `false` | Selecciona Pixel XL, Google y API Android 28. |
| `google_photos.list_mode` | `flat` | `flat`, `calendar`, `month`, `year` o `description_path`. |
| `google_photos.apk_version` | `49029607` | Código de versión que se informa en el protocolo. |
| `google_photos.android_api` | `35` | Nivel de API de Android. |
| `google_photos.download_cache` | `false` | Con `true`, descarga progresiva a un temporal compartido para lecturas aleatorias. |
| `google_photos.encoding` | `Slash,Dot,CrLf,InvalidUtf8` | Máscara de traducción de nombres. El montaje añade las conversiones necesarias para nombres de Windows. |

## Operaciones

- `flat` muestra los medios en la raíz. `calendar` los organiza como `AAAA/AAAA-MM/AAAA-MM-DD` (alias `day`, `week`, `calender`). `month` usa `AAAA/AAAA-MM` (alias `year_month`). `year` usa `AAAA`.
- `description_path` usa etiquetas `{paths:["/viaje","/favoritas"]}` de las descripciones. Copiar los mismos bytes a otra carpeta puede añadir una ruta al medio existente. Al eliminar una ruta, las otras se conservan. Al eliminar la última, el medio se mueve a la papelera. También se leen las etiquetas antiguas `[paths:…]` y `[path:…]`.
- Las carpetas persistentes se derivan de la fecha o de las descripciones. Las carpetas vacías que crea el Explorador son temporales. El nombre y la ubicación de un medio deduplicado, así como la fecha de captura con la que Google lo indexa, los determina la biblioteca.
- Con `download_cache=false`, la lectura es por flujo. Un retroceso abre el flujo de nuevo. Con la caché activada, varios lectores comparten el temporal y pueden leer bytes ya descargados. Se prefiere la URL de la versión original y se usa la alternativa que devuelve Google cuando falta la original.
- Al cerrar el último escritor, la unidad guarda los bytes y el destino en el disco local y confirma la carga en segundo plano. Si se interrumpe la conexión, reintenta. Si se interrumpe el montaje, continúa al montarlo con la misma configuración y letra. Los bytes pendientes quedan en `pending` y el registro indica los errores. `put` devuelve el resultado de la transferencia directamente.
- Una escritura parcial sobre un medio existente usa los bytes originales como base y envía el archivo completo por el mismo flujo de actualización del backend. Windows admite atributos y tiempos locales mientras el medio está pendiente. Google determina la fecha de captura y la versión definitiva. El renombrado de medios ya publicados sigue sin estar disponible.
- `about` consulta la cuota y el recuento. `ls`, `stat`, `metadata` y `hash` exponen el catálogo, identificador, tamaño, metadatos y SHA-1 cuando Google los devuelve. `set-description archivo texto` escribe la descripción completa. Para borrarla desde PowerShell, pasá `--%` antes de `set-description` si tu versión de PowerShell omite el argumento vacío.

```powershell
.\googlephotosfs.exe about
.\googlephotosfs.exe metadata photo.jpg
.\googlephotosfs.exe set-description photo.jpg 'Vacaciones'
.\googlephotosfs.exe get photo.jpg .\descarga.jpg
.\googlephotosfs.exe put .\origen.jpg destino.jpg
.\googlephotosfs.exe sync
```

## Compilación

Instalá Go 1.25 o posterior. En Windows, ejecutá `./build.ps1`. El ejecutable y los archivos del paquete quedan en `dist/`. En otro sistema, usá este comando para compilar para Windows.

```sh
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o googlephotosfs.exe ./src
```

El proyecto también admite el flujo de CMake usado por la referencia.

```sh
cmake -S . -B build
cmake --build build
cmake --install build --prefix dist
```

El proyecto organiza la configuración y el proceso de montaje en `common.go`, `main.go` y `mount_windows.go`. `filesystem_windows.go` registra las operaciones nativas de WinFsp. `store.go` y `catalog.go` resuelven las operaciones y rutas. `photos.go` y los módulos de protocolo, SQLite y transferencias se ejecutan en el mismo proceso. Se eligió Go para compilar todo en un solo binario Windows x64.

Consultá [VALIDACION.md](VALIDACION.md) para la comparación con el backend y el procedimiento con una cuenta real.
