# Docker file for creating a docker container that can run the tests.
#
# Create the image:
#   docker build -t dnss-test -f tests/Dockerfile .
#
# Run the tests:
#   docker run --rm dnss-test
#
# Get a shell inside the image (for debugging):
#   docker run -it --entrypoint=/bin/bash dnss-test

FROM golang:latest

WORKDIR /go/src/blitiri.com.ar/go/dnss

# Make debconf/frontend non-interactive, to avoid distracting output about the
# lack of $TERM.
ENV DEBIAN_FRONTEND noninteractive

RUN apt-get update -q

# Install the required packages for the integration tests.
RUN apt-get install -y -q knot-dnsutils systemd

# Copy into the container. Everything below this line will not be cached.
COPY . .

# Update dependencies to the latest versions, and fetch them to the cache.
# Fetch dependencies to the cache, because we might not have external network
# access once we are running.
# $GO_GET_ARGS allows to create a variant that updates the dependencies to the
# latest versions, for testing.
RUN go get -d -v ${GO_GET_ARGS} ./... && go mod download

# Install dnss, to make sure it is ready to be tested.
RUN go get -d -v . && go install -v .

# Run all the tests.
CMD tests/all.sh
