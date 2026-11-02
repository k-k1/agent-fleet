#!/bin/bash
sub=""; a=("$@"); i=0
while [ $i -lt ${#a[@]} ]; do case "${a[$i]}" in -C|-c) i=$((i+2));; -*) i=$((i+1));; *) sub=${a[$i]}; break;; esac; done
k=other; case "$GIT_DIR" in *af-memory.git) k=memrepo;; esac
case "$*" in *af-memory.git*) k=memrepo;; esac
echo "$EPOCHREALTIME ${AF_GITSRC:-prod} $k $sub" >> /home/dev/.af-work/sange6k/m/gitcalls.log
exec /usr/bin/git "$@"
