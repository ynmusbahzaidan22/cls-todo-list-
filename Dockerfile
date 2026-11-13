FROM golang:1.27.3-alpine AS build

WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/todo-api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=build --chown=nonroot:nonroot /out/todo-api /app/todo-api
COPY --from=build --chown=nonroot:nonroot /src/frontend /app/frontend
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app/todo-api"]
