# VeloStream

VeloStream es una API y pequeño servicio para descargar y servir contenido de YouTube (vídeo/audio) de forma privada y controlada. Pensado como una herramienta ligera para usuarios autenticados y para integraciones internas.

**Estado:** Estable (básico)

## Características

- Descarga video en múltiple calidad (1080/720/480/360) en MP4.
- Extrae audio en formato MP3.
- Endpoints con autenticación basada en Paseto v2.
- Límite de descargas por usuario (ratelimit).
- Limpieza automática de archivos temporales.
- Soporta ejecución en Docker.

## Requisitos

- Go 1.20+
- `yt-dlp` (en PATH)
- `ffmpeg` (en PATH)
- PostgreSQL (opcional, puede usarse el conexión por defecto en `DATABASE_URL`)
- `gh` (GitHub CLI) para publicar el repo fácilmente (opcional)

## Instalación (desarrollo)

1. Clona el repo o copia los archivos al directorio de trabajo.
2. Configura variables de entorno (opcional):

- `DATABASE_URL` — cadena de conexión PostgreSQL
- `PORT` — puerto del servidor (por defecto `3000`)
- `CORS_ORIGIN` — orígenes permitidos para CORS

3. Instala dependencias y ejecuta:

```bash
go mod download
go run main.go
```

O construye el binario:

```bash
go build -o velostream ./
./velostream
```

## Endpoints principales

- `POST /register` — registra un usuario
- `POST /login` — obtiene token Paseto
- `GET /download?id=<videoID>&quality=<360|480|720|1080|mp3>` — descarga un recurso (requiere `Authorization: Bearer <token>`)
- `GET /download/video/info?id=<videoID>` — obtiene metadatos del vídeo (requiere auth)

## Uso con Docker

Se incluye una configuración de ejemplo en `docker/`.

Construir y ejecutar con Docker Compose:

```bash
docker compose up --build
```

## Seguridad y consideraciones legales

- Este proyecto usa `yt-dlp` para obtener contenidos. Asegúrate de cumplir las leyes y términos de servicio aplicables cuando descargues o redistribuyas contenido.
- Protege las claves, tokens y el acceso a la base de datos.

## Contribuciones

Agradezco PRs pequeñas y bien documentadas. Abre un issue para discutir cambios mayores.

## Licencia

Este proyecto está bajo la licencia MIT. Ver el archivo `LICENSE`.

---

Mantenedor: Proyecto local `velostream` — adapta la configuración a tu entorno.
