FROM python:3.13-alpine@sha256:399babc8b49529dabfd9c922f2b5eea81d611e4512e3ed250d75bd2e7683f4b0

WORKDIR /app

RUN pip install --no-cache-dir pyftpdlib==2.0.1

COPY ftp_server.py /app/ftp_server.py
COPY data /srv/ftp

EXPOSE 21 32100-32109

CMD ["python", "/app/ftp_server.py"]
