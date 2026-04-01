# The Go Programming Language

This is a fork of Go - an open source programming language and runtime - https://github.com/golang/go
. To be precise, this repository forks the branch `release-branch.go1.25`, i.e., Go version 1.25. To compile this repository you need Go version 1.22.7 installed on your system, as the bootstrap. Follow the below instructions to compile the Go runtime in this repository

## Download Go v1.22.7

It is assumed that we want to compile for Linux, 64-bit Intel/AMD machine. Download the respective go package.
```
cd ~/
wget https://go.dev/dl/go1.22.7.linux-amd64.tar.gz
mkdir -p ~/bootstrap_go
sudo tar -C ~/bootstrap_go -xzf ~/go1.22.7.linux-amd64.tar.gz
```

## Compile the Go in this repository
```
git clone https://github.gatech.edu/HeteroBench/go.git ~/mygo
cd ~/mygo/src
GOROOT_BOOTSTRAP=~/bootstrap_go/go ./make.bash
```

The above command will compile Go in this repository and the build will be stored in `~/mygo/bin/go`.

NOTE: Using `all.bash` builds and runs the tests on your build. I saw that some of these tests fail, even if no changes are made to the runtime. Hence, we skip running the unit tests present in the Go repository.