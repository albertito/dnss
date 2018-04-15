#!/bin/bash
#
# Run tests in coverage mode, generating HTML and function reports.
#

set -e

# The tests are run from the repository root.
cd "$(realpath `dirname ${0}`)/../"

# Recreate the coverage output directory, to avoid including stale results
# from previous runs.
rm -rf .coverage
mkdir -p .coverage
export COVER_DIR="$PWD/.coverage"


go test \
	-covermode=count \
	-coverprofile="$COVER_DIR/pkg-tests.out" \
	-coverpkg=./... \
	./...

# These will run in coverage mode due to $COVER_DIR being set.
setsid -w ./tests/external.sh

# Merge all coverage output into a single file.
go run "tests/gocovcat.go" .coverage/*.out \
        > .coverage/all.out

go tool cover -func=.coverage/all.out | sort -k 3 -n > ".func.txt"
go tool cover -html=.coverage/all.out -o .coverage/dnss.cover.html

grep -i total .func.txt
echo "file:///$PWD/.coverage/dnss.cover.html"
