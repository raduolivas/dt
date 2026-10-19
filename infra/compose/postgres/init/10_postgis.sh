#!/bin/sh
# Intentionally empty. It replaces the postgis/postgis image's own 10_postgis.sh,
# which installs PostGIS, topology and the tiger geocoder into the public schema
# and fails because 01-schemas.sql already installed PostGIS into "extensions".
