#!/bin/bash

set -e
set -x

rm -rf build/opflex/dist
mkdir -p build/opflex/dist

pushd build/opflex

# Build OpFlex and OVS binaries
if [ -d opflex ]; then
    pushd opflex
    git pull
    popd
else
    git clone https://git.opendaylight.org/gerrit/opflex.git --depth 1
fi
pushd opflex/genie
mvn compile exec:java
popd
cp ../../docker/Dockerfile-opflex-build-ubi opflex
docker build ${DOCKER_BUILD_ARGS} --build-arg make_args="${MAKE_ARGS}" -t noiro/opflex-build-ubi -f opflex/Dockerfile-opflex-build-ubi opflex
docker run noiro/opflex-build-ubi tar -c -C /usr/local \
       bin/opflex_agent bin/gbp_inspect bin/mcast_daemon \
    | tar -x -C dist
docker run -w /usr/local noiro/opflex-build /bin/sh -c 'find lib \(\
         -name '\''libopflex*.so*'\'' -o \
         -name '\''libmodelgbp*so*'\'' -o \
         -name '\''libopenvswitch*so*'\'' -o \
         -name '\''libsflow*so*'\'' -o \
         -name '\''libprometheus-cpp-*so*'\'' -o \
         -name '\''libofproto*so*'\'' \
         \) ! -name '\''*debug'\'' \
        | xargs tar -c ' \
    | tar -x -C dist
docker run -w /usr/local noiro/opflex-build /bin/sh -c \
       'find lib bin -name '\''*.debug'\'' | xargs tar -cz' \
    > debuginfo.tar.gz
cp ../../docker/launch-opflexagent.sh dist/bin/
cp ../../docker/launch-mcastdaemon.sh dist/bin/

# Build the minimal OpFlex container
cp ../../docker/Dockerfile-opflex dist
docker build ${DOCKER_BUILD_ARGS} -t noiro/opflex -f dist/Dockerfile-opflex dist

popd
