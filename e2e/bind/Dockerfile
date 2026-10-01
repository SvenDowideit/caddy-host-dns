# syntax=docker/dockerfile:1

# BIND authoritative server for the e2e test, with a pre-created example.com
# zone that accepts RFC2136 dynamic updates authenticated by a TSIG key.
#
# Build context is the repository root.
FROM internetsystemsconsortium/bind9:9.18

COPY e2e/bind/named.conf /etc/bind/named.conf
COPY e2e/bind/db.example.com /var/cache/bind/db.example.com
COPY e2e/bind/tsig.key /etc/bind/tsig.key

# The named process drops to the "bind" user; make the zone directory and files
# writable so dynamic updates can create the journal.
RUN chown -R bind:bind /var/cache/bind && \
    chmod 755 /var/cache/bind && \
    chmod 644 /var/cache/bind/db.example.com && \
    chown root:bind /etc/bind/tsig.key && chmod 640 /etc/bind/tsig.key
