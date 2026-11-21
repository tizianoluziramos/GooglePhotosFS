# Validación de GooglePhotosFS

Referencias revisadas. [TelegramFS 9e76dfa](https://github.com/tizianoluziramos/TelegramFS/tree/9e76dfa151784aa414c9130cfc83b75b010cb521) · [rclone-gphotos-oot 1ee56f5](https://github.com/akhil-rana/rclone-gphotos-oot/tree/1ee56f51f4a49ff0dcf7d275039cf304b2cdf892)

| Capacidad original | Implementación y comprobación |
| --- | --- |
| Montaje | Disco nativo WinFsp, ACL del usuario, operaciones de archivo WinFsp y señal de desmontaje. Compilado para Windows x64. El flujo de integración instala WinFsp Core y prueba el volumen con operaciones de Windows. La ejecución del controlador queda pendiente en un host Windows. |
| Autenticación | Cookie manual, navegador Linux aislado, importación de credencial, intercambio Android y renovación de bearer. Intercambio, renovación, reutilización, selección de cookie y control de ámbitos probados localmente. Inicio de sesión real pendiente. |
| API móvil | Solicitudes Protobuf con gzip, máscaras de consulta, autenticación, dispositivo, errores HTTP y reintentos. Se compararon hashes SHA-256 de once solicitudes producidas por los constructores originales. El contenido dinámico de confirmación se verificó en la prueba de transferencias. |
| Caché SQLite | Esquema de medios compatible, migración de columnas, estado inicial, reanudación, páginas diferenciales, borrados y actualización transaccional del cursor. Probado localmente con datos de prueba. |
| Listados y consulta | Cinco modos, alias, subraíz, fecha UTC, desduplicación de objetos canónicos, colisiones de nombre, sufijos por identificador y conversiones de nombres para Windows. Pruebas locales y comparación diferencial de la traducción de nombres con el codificador de la dependencia original, rclone 1.75.2. |
| Cargas | SHA-1, consulta remota de duplicados, sesión de carga, PUT y confirmación en calidad original. Perfil normal o Pixel XL. Flujo probado localmente. Las escrituras parciales en el montaje se transforman en la misma operación de actualización completa del backend. Se comprobó la persistencia local y la reapertura de cargas cerradas. Google decide la cuota y el almacenamiento efectivo. |
| Descargas | Selección de recurso original/editado/vídeo, flujo directo e índice temporal compartido. La prueba local compara bytes y valida lecturas aleatorias con dos identificadores abiertos. |
| Metadatos | Pie de foto, dimensiones, captura, creación, ubicación, cámara, vídeo, estado y otros campos del índice. Escritura de la descripción y consulta de SHA-1 e identificador. Pruebas locales de análisis y actualización. |
| Directorios y papelera | Etiquetas antiguas y actuales, añadir ruta al deduplicar, quitar un vínculo y papelera en la última ruta. Probado en el flujo local. Los directorios nuevos del montaje existen en la sesión hasta que una carga determina su ruta virtual. |
| Cuota | Bytes totales, usados, libres y recuento opcionales. Probado con la respuesta local de cuota. |
| Comportamientos sin soporte upstream | Google determina los tiempos de los medios. El backend carece de renombrado remoto y de carpetas persistentes vacías. El montaje puede renombrar un archivo local todavía pendiente. |

## Qué cubrió el entorno disponible

- Ejecución de la suite de Go en Linux, incluidos SQLite, los mensajes de referencia y el flujo HTTP contra un servidor local de pruebas. Las respuestas locales ejercitan el código de producción.
- Compilación del ejecutable y de la suite de pruebas para Windows x64. Instalación del paquete con CMake e inspección del resultado PE32+.
- Prueba de Windows incorporada al flujo de integración. Usa una unidad real de WinFsp con respuestas HTTP de prueba para separar la validación del controlador de la autenticación de Google. Cubre listado, acceso por nombre sin distinción de mayúsculas, lectura por desplazamientos, escritura parcial, truncado, eliminación, reapertura de cargas pendientes y solo lectura.
- Los endpoints de autenticación y biblioteca respondieron HTTP 403 a peticiones sin credenciales. No se verificó una operación autenticada.
- El entorno de trabajo ejecuta Linux. La sesión no incluía un host Windows con WinFsp ni credenciales de Google Photos. El montaje, el inicio de sesión, la carga, la descarga, el efecto sobre la cuota y la papelera quedan sin verificación real.

## Prueba en Windows con Google Photos

La prueba de integración del controlador se puede ejecutar en Windows con WinFsp Core y Go.

```powershell
$env:GOOGLEPHOTOSFS_TEST_WINFSP = '1'
go test -count=1 -v -run '^TestWinFspVolume$' ./src
```

Con WinFsp Core instalado, autenticá una cuenta de pruebas y seleccioná `flat` sin subraíz. Usá una foto o vídeo válido que no exista aún en Google Photos. El script crea un nombre único, monta la unidad, prueba que puede leerla y escribirla, comprueba el hash de los bytes descargados, consulta cuota y metadatos, escribe una descripción y desmonta la unidad. La opción `-TrashAfterTest` mueve únicamente el medio de prueba a la papelera.

```powershell
./build.ps1
./tests/verify-windows.ps1 -Media C:\Pruebas\nueva-foto.jpg -Drive P: -TrashAfterTest
```

Para validar los otros modos, alterná el modo y desmontá entre ejecuciones. Compará los directorios con la fecha de captura UTC. Con `description_path`, copiá un mismo medio nuevo a dos directorios, comprobá que ambos muestran el mismo identificador, eliminá una de las rutas y verificá que la otra sigue accesible. Para comparar el flujo y la caché de descarga, repetí las lecturas con `download_cache` en ambos estados.
