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
mkdir -p .coverage/sh .coverage/go .coverage/all
export COVER_DIR="$PWD/.coverage"

# Run Go tests in coverage mode.
go test \
	-covermode=count \
	-coverpkg=./... \
	./... \
	-args -test.gocoverdir=${COVER_DIR}/go/

# Run external tests in coverage mode. They will run in coverage mode due to
# $GOCOVERDIR being set.
GOCOVERDIR="${COVER_DIR}/sh" setsid -w ./tests/external.sh

# Merge all coverage output into a single file.
go tool covdata merge -i "${COVER_DIR}/go,${COVER_DIR}/sh" -o "${COVER_DIR}/all"
go tool covdata textfmt -i "${COVER_DIR}/all" -o "${COVER_DIR}/merged.out"

# Generate reports based on the merged output.
go tool cover -func=.coverage/merged.out | sort -k 3 -n > ".func.txt"
go tool cover -html=.coverage/merged.out -o .coverage/dnss.cover.html

grep -i total .func.txt
echo "file:///$PWD/.coverage/dnss.cover.html"
