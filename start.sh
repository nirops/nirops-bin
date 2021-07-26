curl -O https://raw.githubusercontent.com/nirops/nirops-bin/main/nirops
chown +x nirops
./nirops authtoken $1
./nirops
