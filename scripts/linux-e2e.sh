#!/bin/sh
# linux-e2e.sh: run `make check` and `make e2e` on Linux, in a Multipass VM.
#
# Usage: scripts/linux-e2e.sh [--fresh] [--delete] [--check-only]
#
#   --fresh       delete the VM first and create it again
#   --delete      delete the VM afterwards
#   --check-only  run only `make check`, not `make e2e`
#
# The VM is a real Linux host: Docker runs natively in it, so the e2e-*
# containers that `make e2e` starts are reachable at 127.0.0.1 as on the Mac.
# It is created on first use and kept running for fast re-runs.
#
# What runs is HEAD, not the working tree: a git bundle of it and the tags
# is cloned into the checkout's own directory in the VM, so the build sees
# the same commit (and `git describe`) as here, and web/node_modules is
# Linux's own. Uncommitted changes are left out, with a warning.
#
# Runs take turns: each holds a lock on this host, per VM, from before it
# touches the VM until it exits, so no run deletes the VM, or changes its
# toolchains, under another.
#
# Environment:
#   BOTTOWN_LINUX_VM      VM name (bottown-linux)
#   BOTTOWN_LINUX_CPUS    CPUs for a new VM (4)
#   BOTTOWN_LINUX_MEMORY  memory for a new VM (8G)
#   BOTTOWN_LINUX_DISK    disk for a new VM (30G)
#   BOTTOWN_LINUX_IMAGE   Ubuntu image for a new VM (26.04)
#   BOTTOWN_LINUX_NODE    Node.js major version (this host's, else 26)
#   BOTTOWN_LINUX_LOG     where to write the output
#                         (<gitdir>/bottown/linux-e2e.log)
#
# Exits with the status of the checks in the VM.
set -eu

vm=${BOTTOWN_LINUX_VM:-bottown-linux}
cpus=${BOTTOWN_LINUX_CPUS:-4}
memory=${BOTTOWN_LINUX_MEMORY:-8G}
disk=${BOTTOWN_LINUX_DISK:-30G}
image=${BOTTOWN_LINUX_IMAGE:-26.04}
# web/ declares no Node version, and its tests use what the host's Node
# accepts (node --test with a directory needs 25 or later): match it.
hostnode=$(node --version 2>/dev/null | sed -n 's/^v\([0-9]*\)\..*/\1/p')
node=${BOTTOWN_LINUX_NODE:-${hostnode:-26}}

fresh=0 delete=0 targets="check e2e"
for a in "$@"; do
	case $a in
	--fresh) fresh=1 ;;
	--delete) delete=1 ;;
	--check-only) targets=check ;;
	-h | --help)
		sed -n '2,/^set -eu/p' "$0" | sed '$d; s/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "linux-e2e: unknown argument $a" >&2
		exit 2
		;;
	esac
done

# say prints a progress line with the time, so slow phases show where they are.
say() { echo "linux-e2e $(date +%H:%M:%S): $*"; }
die() {
	say "$*" >&2
	exit 1
}

# Re-run this script holding the VM's host lock, a kernel lock that is
# released however the holder exits: flock(1) on Linux, lockf(1) on macOS.
lock=/tmp/bottown-linux-e2e-$vm.lock
if [ "${BOTTOWN_LINUX_E2E_LOCK:-}" != "$lock" ]; then
	BOTTOWN_LINUX_E2E_LOCK=$lock
	export BOTTOWN_LINUX_E2E_LOCK
	if command -v flock >/dev/null 2>&1; then
		flock -n "$lock" true || say "waiting for another run using $vm"
		exec flock "$lock" sh "$0" "$@"
	elif command -v lockf >/dev/null 2>&1; then
		lockf -k -s -t 0 "$lock" true || say "waiting for another run using $vm"
		exec lockf -k "$lock" sh "$0" "$@"
	fi
	die "needs flock(1) or lockf(1) to take turns with other runs"
fi

command -v multipass >/dev/null 2>&1 ||
	die "needs Multipass (https://multipass.run; brew install --cask multipass)"
top=$(git rev-parse --show-toplevel) || die "not inside a git checkout"
cd "$top"
gitdir=$(git rev-parse --absolute-git-dir)
log=${BOTTOWN_LINUX_LOG:-$gitdir/bottown/linux-e2e.log}
mkdir -p "$(dirname "$log")"

head=$(git rev-parse HEAD)
gover=$(sed -n 's/^toolchain go//p' go.mod)
[ -n "$gover" ] || gover=$(sed -n 's/^go //p' go.mod)
[ -n "$gover" ] || die "no go version in go.mod"

# The checkout's directory in the VM: its basename and a hash of its path,
# so worktrees with the same name in different fams stay apart.
key=$(basename "$top")-$(printf %s "$top" | cksum | cut -d' ' -f1)
work=/home/ubuntu/work/$key

[ -z "$(git status --porcelain)" ] ||
	say "uncommitted changes are not tested; testing HEAD $head" >&2

vmexec() { multipass exec "$vm" -- "$@"; }
exists() { multipass info "$vm" >/dev/null 2>&1; }

delete_vm() {
	exists || return 0
	say "deleting $vm"
	multipass delete --purge "$vm"
}

# wait_init waits for cloud-init to finish in the VM. It polls for the file
# cloud-init writes last: `multipass exec ... cloud-init status --wait` can
# spin on the host without returning.
wait_init() {
	vmexec sh -c 'n=0; until [ -f /var/lib/cloud/instance/boot-finished ]; do
		n=$((n + 1)); [ $n -lt 450 ] || { echo "cloud-init did not finish" >&2; exit 1; }; sleep 2; done' ||
		die "cloud-init did not finish in $vm; try --fresh"
	vmexec sh -c 'command -v docker >/dev/null' || die "cloud-init did not install Docker in $vm; try --fresh"
}

launch_vm() {
	if exists; then
		case $(multipass info "$vm" --format csv | sed -n 2p | cut -d, -f2) in
		Running) say "reusing $vm" ;;
		*)
			say "starting $vm"
			multipass start "$vm"
			;;
		esac
		# A launch that was interrupted may have left cloud-init running.
		wait_init
		return 0
	fi
	say "launching $vm ($image, $cpus CPUs, $memory, $disk); the first launch downloads the image"
	init=$(mktemp "${TMPDIR:-/tmp}/linux-e2e-init.XXXXXX")
	cat >"$init" <<'EOF'
#cloud-config
package_update: true
packages: [docker.io, docker-buildx, git, make, curl, ca-certificates, xz-utils]
runcmd:
  - usermod -aG docker ubuntu
  - systemctl enable --now docker
EOF
	rc=0
	multipass launch "$image" --name "$vm" --cpus "$cpus" --memory "$memory" \
		--disk "$disk" --cloud-init "$init" --timeout 900 || rc=$?
	rm -f "$init"
	[ $rc = 0 ] || return $rc
	say "waiting for cloud-init to install Docker, git and make"
	wait_init
	say "$vm is up"
}

# tools installs the Go that go.mod names and the newest Node of the wanted
# major, each once per version under /opt, and prints the PATH entries for
# them: each run uses its own, never links shared with another.
tools() {
	say "checking Go $gover and Node $node in $vm" >&2
	vmexec sudo sh -eu -c '
gover=$1 node=$2
say() { echo "linux-e2e $(date +%H:%M:%S): $*" >&2; }
case $(uname -m) in aarch64) arch=arm64 narch=arm64 ;; x86_64) arch=amd64 narch=x64 ;; *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;; esac
if [ ! -x /opt/go-$gover/bin/go ]; then
	say "installing Go $gover"
	rm -rf /opt/go-$gover.tmp && mkdir -p /opt/go-$gover.tmp
	curl -fsSL https://go.dev/dl/go$gover.linux-$arch.tar.gz | tar xz -C /opt/go-$gover.tmp --strip-components=1
	mv /opt/go-$gover.tmp /opt/go-$gover
fi
base=https://nodejs.org/dist/latest-v$node.x
file=$(curl -fsSL $base/SHASUMS256.txt | awk -v a="linux-$narch.tar.xz" "index(\$2, a) { print \$2 }")
[ -n "$file" ] || { echo "no Node $node for linux-$narch" >&2; exit 1; }
dir=/opt/${file%.tar.xz}
if [ ! -x $dir/bin/node ]; then
	say "installing $file"
	rm -rf $dir.tmp && mkdir -p $dir.tmp
	curl -fsSL $base/$file | tar xJ -C $dir.tmp --strip-components=1
	mv $dir.tmp $dir
fi
echo "/opt/go-$gover/bin:$dir/bin"
' sh "$gover" "$node"
}

# test_head clones HEAD into the checkout's directory, a fresh clone each time
# keeping only web/node_modules (npm ci refreshes it when the lock file
# changes), and runs the targets there as ubuntu (sudo -u gives it the docker
# group), with the toolchains in path first. run calls it on the left of
# ||, where errexit is off, so each step here checks its own status.
test_head() {
	path=$1
	say "copying $head to $vm:$work"
	vmexec mkdir -p "$work" || return
	bundle=$(mktemp "${TMPDIR:-/tmp}/linux-e2e-bundle.XXXXXX")
	git bundle create "$bundle" HEAD --tags 2>/dev/null &&
		vmexec sh -c "cat >'$work.bundle'" <"$bundle" || {
		rm -f "$bundle"
		return 1
	}
	rm -f "$bundle"
	vmexec sudo -u ubuntu -H env PATH="$path:/usr/local/bin:/usr/bin:/bin" sh -eu -c '
say() { echo "linux-e2e $(date +%H:%M:%S): $*"; }
work=$1 head=$2 targets=$3
# A global git identity, as a developer machine has: bottown commits as the
# user (clone --agents-md), and e2e expects that to work.
git config --global user.name >/dev/null || git config --global user.name "bottown linux-e2e"
git config --global user.email >/dev/null || git config --global user.email linux-e2e@bottown.invalid
say "cloning into $work/src"
cd "$work"
if [ -d src/web/node_modules ]; then mv src/web/node_modules node_modules.keep; fi
rm -rf src
git clone -q "$work.bundle" src
rm -f "$work.bundle"
git -C src checkout -q --detach "$head"
if [ -d node_modules.keep ]; then mv node_modules.keep src/web/node_modules; fi
cd src
say "$(uname -sm), $(go version), node $(node --version), $(docker --version), $(git describe --always --abbrev=12)"
for t in $targets; do
	say "make $t"
	t0=$(date +%s)
	make "$t"
	say "make $t passed in $(($(date +%s) - t0))s"
done
' sh "$work" "$head" "$targets"
}

# run does it all, failing at the first step that fails. --delete removes
# the VM even after a failed test.
run() {
	[ $fresh = 0 ] || delete_vm
	launch_vm
	path=$(tools)
	rc=0
	test_head "$path" || rc=$?
	[ $delete = 0 ] || delete_vm
	return $rc
}

# Everything run prints, setup included, goes to the log too. run is called
# in a subshell of its own with errexit on, so that any step's failure stops
# it, and its status is kept in a file, since a pipeline's is tee's.
st=$(mktemp "${TMPDIR:-/tmp}/linux-e2e-status.XXXXXX")
{
	set +e
	(
		set -e
		run
	)
	echo $? >"$st"
} 2>&1 | tee "$log"
status=$(cat "$st")
rm -f "$st"

if [ "$status" = 0 ]; then
	say "passed at $head (log: $log)"
else
	say "FAILED at $head (log: $log)" >&2
fi
exit "$status"
