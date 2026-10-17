#!/bin/sh
# A stand-in for packer, put first on PATH by packer_test.go. It records
# each call and the token it was given, and does what the FAKEPACKER_*
# variables tell it to:
#   FAKEPACKER_LOG       file each call is appended to
#   FAKEPACKER_FAIL      subcommand that exits 1
#   FAKEPACKER_MANIFEST  written to packer-manifest.json by build
#   FAKEPACKER_HANG      subcommand that waits until interrupted
cmd=$1
echo "argv: $*" >>"$FAKEPACKER_LOG"
echo "token: ${DIGITALOCEAN_TOKEN-unset}" >>"$FAKEPACKER_LOG"
echo "pkr_var: ${PKR_VAR_do_token-unset}" >>"$FAKEPACKER_LOG"
echo "==> fake packer $cmd"
echo "fake packer $cmd on stderr" >&2

if [ "$cmd" = build ] && [ -n "$FAKEPACKER_MANIFEST" ]; then
	printf '%s' "$FAKEPACKER_MANIFEST" >packer-manifest.json
fi
if [ "$cmd" = "$FAKEPACKER_HANG" ]; then
	trap 'echo "interrupted: $cmd" >>"$FAKEPACKER_LOG"; exit 130' INT
	echo "hanging: $cmd" >>"$FAKEPACKER_LOG"
	while :; do sleep 0.05; done
fi
if [ "$cmd" = "$FAKEPACKER_FAIL" ]; then
	exit 1
fi
exit 0
