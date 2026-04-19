FROM alpine:latest

RUN apk add --no-cache ca-certificates ffmpeg yt-dlp

WORKDIR /app

COPY velostream .

RUN chmod +x velostream

EXPOSE 3000

CMD ["./velostream"]