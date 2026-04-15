FROM golang:1.25-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/AutoOpenTelegramPremium .

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /out/AutoOpenTelegramPremium /app/AutoOpenTelegramPremium

RUN mkdir -p /app/data

EXPOSE 8080

ENTRYPOINT ["/app/AutoOpenTelegramPremium"]
