#!/bin/sh
set -eu
make control-e2e
echo "Automated failure suite passed: single output retry, source loss/return and bounded recovery"
