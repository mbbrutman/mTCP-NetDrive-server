#!/bin/sh
#
# I'm providing this shell script because I'm too lazy to provide something
# more sophisticated.  (Thanks to Tim S. for helping me clean up the original script.)
#
# Go has marvelous support for cross compiling, so use it!  To do so set the
# GOOS and GOARCH environment variables.
#
# * If you set GOOS then set GOARCH too.  (This script assumes you did.)
# * If you set GOARCH=arm then you should set GOARM for specific hardware versions
#
# Examples:
#
#   ./build.sh                       Builds for whatever the current OS and architecture is.
#   GOOS=linux GOARCH=ppcle64        Builds for Linux on little-endian PPC64
#   GOOS=linux GOARCH=arm GOARM=5    Builds for older Linux on older ARM processors.
#   GOOS=linux GOARCH=arm64          Builds for older Linux on 64 bit ARM.
#
# Use "go tool dist list" to see what targets your specific version of Go supports.
#
# Questions or comments? mbbrutman@gmail.com


builddate="$(TZ='America/Los_Angeles' date +'%b %d %Y')"
go build -ldflags="-s -w -X 'brutman.com/brutman/netdrv/globals.BuildDate=${builddate}'" -o netdrive brutman.com/brutman/netdrv
if [ "$?" -eq 0 ]; then
  if [ -n "$GOOS" ]; then
    if [ $GOARCH == "arm" ]; then
      mv netdrive netdrive_${GOOS}_${GOARCH}_${GOARM}
    else
      mv netdrive netdrive_${GOOS}_${GOARCH}
    fi
  elif file netdrive | grep -q Windows; then
    mv netdrive netdrive.exe
  fi
fi
