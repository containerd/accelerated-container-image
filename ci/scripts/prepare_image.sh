#!/bin/bash

from=${1:?}
to=${2:?}

set -x

ctr i pull "${from}"
ctr i tag "${from}" "${to}"
ctr i push "${to}"
ctr i rm "${from}" "${to}"
