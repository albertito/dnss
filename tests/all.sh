#!/bin/bash

# The tests are run from the repository root.
cd "$(realpath "$(dirname "$0")/../" )"

set -ev

#####################################
go test ./...

#####################################
setsid -w tests/bench

#####################################
setsid -w tests/external.sh

#####################################
setsid -w tests/coverage.sh

