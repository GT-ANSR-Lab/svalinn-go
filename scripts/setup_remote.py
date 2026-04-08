#!/usr/bin/env python3
import os
import paramiko
from util import *
from config_remote import *

k = paramiko.RSAKey.from_private_key_file(KEY_LOCATION)

# config check
if len(NODES) < 1:
    print("[ERROR] There is no server to configure.")
    exit()

# change default shell to bash
print("Changing default shell to bash...")
conns = []
for node in NODES:
    node_conn = paramiko.SSHClient()
    node_conn.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    node_conn.connect(hostname = node["name"], username = USERNAME, pkey = k)
    conns.append(node_conn)

execute_remote(conns, "sudo usermod -s /bin/bash {}".format(USERNAME), True, False)

for conn in conns:
    conn.close()

# connections to servers
conns = []
for node in NODES:
    node_conn = paramiko.SSHClient()
    node_conn.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    node_conn.connect(hostname = node["name"], username = USERNAME, pkey = k)
    conns.append(node_conn)

print("Cleaning up machines...")
cmd = "sudo killall -9 cstate"
execute_remote(conns, cmd, True, False)

cmd = "sudo rm -rf ~/{}".format(ARTIFACT_PATH)
execute_remote(conns, cmd, True, False)

# distributing code-base
print("Distributing sources...")
repo_name = (os.getcwd().split('/'))[-1]
for server in NODES:
    cmd = "rsync -azh -e \"ssh -i {} -o StrictHostKeyChecking=no"\
            " -o UserKnownHostsFile=/dev/null\" --info=progress2 --exclude outputs/ ../{}/"\
            " {}@{}:~/{}"\
            .format(KEY_LOCATION, repo_name, USERNAME, server, ARTIFACT_PATH)
    execute_local(cmd)

# install the dependencies
print("Installing dependencies...")
cmd = "sudo apt-get update"
execute_remote(conns, cmd, True)

cmd = "sudo apt -y install meson ninja-build"
execute_remote(conns, cmd, True)

cmd = "sudo apt-get -y install build-essential libnuma-dev clang autoconf"\
        " autotools-dev m4 automake libevent-dev  libpcre++-dev libtool"\
        " ragel libev-dev moreutils parallel cmake python3 python3-pip"\
        " libjemalloc-dev libaio-dev libdb5.3++-dev numactl hwloc libmnl-dev"\
        " libnl-3-dev libnl-route-3-dev uuid-dev libssl-dev libcunit1-dev pkg-config"\
        " intel-cmt-cat"
execute_remote(conns, cmd, True)
cmd = "pip3 install pandas openpyxl xlrd --break-system-packages"
execute_remote(conns, cmd, True)

# configure the machines to work in high performance mode
cmd = "sudo ~/{}/scripts/setup_machine.sh".format(ARTIFACT_PATH)
execute_remote(conns, cmd, True)

# build bootstrap go (required to build our custom go runtime)
print("Installing Bootstrap Go...")
cmd = "cd ~/{}/deps && wget https://go.dev/dl/go1.22.7.linux-amd64.tar.gz &&"\
    " rm -rf bootstrap &&"\
    " mkdir -p bootstrap &&"\
    " tar -C bootstrap -xzf go1.22.7.linux-amd64.tar.gz "\
    .format(ARTIFACT_PATH)
print(cmd)
execute_remote(conns, cmd, True)

# XXX: This command needs an update (currently I built manually)
# build go runtime
print("Building Go runtime...")
cmd = "GOROOT_BOOTSTRAP=~/{}/deps/bootstrap/go"\
    " bash -c 'cd ~/{}/go/src && ./make.bash'".\
    format(ARTIFACT_PATH, ARTIFACT_PATH)
print(cmd)
execute_remote(conns, cmd, True)

print("Done.")
