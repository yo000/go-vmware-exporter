
# Start from golang v1.11 base image
FROM golang:1.19 as builder

# Add Maintainer Info
LABEL maintainer="<johan@nosd.in>"

# Set the Current Working Directory inside the container
WORKDIR /go/src/github.com/yo000/go-vmware-exporter

# Copy everything from the current directory to the PWD(Present Working Directory) inside the container
COPY . .

# Download dependencies
RUN go get -d -v ./...

# Build the Go app
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o /go/bin/go-vm .


######## Start a new stage from scratch #######
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root/

# Copy the Pre-built binary file from the previous stage
COPY --from=builder /go/bin/go-vm .

COPY --from=builder /go/src/github.com/yo000/go-vmware-exporter/config.yaml .

EXPOSE 8080

CMD ["./go-vm"]
