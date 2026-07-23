# Dave

Dave is a data backup and archival tool.

---

Dave was originally designed to create backups of Hemi op-geth node data directories. As such, it was designed with the
following in mind:

1. Data safety - To prevent corrupted backups, Dave can stop a container prior to cloning directories. Once cloned, Dave
   starts the container and waits until healthchecks pass (ensuring the data directories are healthy) before continuing.

2. Container downtime restrictions - Dave clones the data directories using rsync, reducing the time the container must
   be offline to safely copy data. This has a trade-off of requiring more disk to store data, as a full copy will be
   made and retained, however it reduces the time spent reading files directly from the node data directories.

3. Handle failures - If Dave is unable to create a working backup, it will trigger alerts and attempt to start the node.
   No further action is taken by Dave.

## Building from Source

### Prerequisites

- `git`
- `make`
- [Go v1.26+](https://go.dev/dl/)

### Building with Makefile

1. Clone the `dave` repository:
   ```shell
   git clone https://github.com/hemilabs/dave.git
   cd dave
   ```

2. Setup and build binaries:
   ```shell
   make deps    # Download and install dependencies
   make install # Build binaries
   ```

Output binaries will be written to the `bin/` directory.

## Usage

```shell
# Create a new snapshot containing path1 and path2
dave backup -r local:./backup/ /path1 /path2

# Create another snapshot:
#  - Compress archives with Zstandard
#  - Stop container while copying data
dave backup -r local:./backup \
    --compression zstd --container-id c9af3b2dbb63 \
    /path1 /path2

# List backups in the repository
dave ls -r local:./backup
# Snapshot ID                       Time                 Archives              
# -----------------------------------------------------------------------------
# d3bd1e2194a47b3e61c8ce220ac3e41b  2025-06-17 19:13:46  path2.tar.gz (129 KiB)
#                                                        path1.tar.gz (128 KiB)
# da2f0b9ccc3af0bcf2eb077e7847c4f6  2025-06-17 19:14:48  path2.tar.zst (53 KiB)
#                                                        path1.tar.zst (53 KiB)
# -----------------------------------------------------------------------------
# 2 snapshots

# Apply a retention policy to the repository
dave forget -r local:./backup --keep-last 1
# keep 1 snapshots:
# Snapshot ID                       Time                 Archives       Keep Reason  
# -----------------------------------------------------------------------------------
# da2f0b9ccc3af0bcf2eb077e7847c4f6  2025-06-17 19:14:48  path2.tar.zst  last snapshot
#                                                        path1.tar.zst               
# -----------------------------------------------------------------------------------
#
# remove 1 snapshots:
# Snapshot ID                       Time                 Archives    
# -------------------------------------------------------------------
# d3bd1e2194a47b3e61c8ce220ac3e41b  2025-06-17 19:13:46  path2.tar.gz
#                                                        path1.tar.gz
# -------------------------------------------------------------------
```

## License

This project is licensed under the [MIT License](https://github.com/hemilabs/dave/blob/main/LICENSE).
