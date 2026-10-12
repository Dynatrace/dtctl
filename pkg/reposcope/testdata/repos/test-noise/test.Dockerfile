FROM gcr.example.invalid/golang
COPY . /src
ENTRYPOINT ["/src/bin/platform-service-integration"]
