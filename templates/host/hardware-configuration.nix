# Replace this file. It is the one part of a NixOS config nobody can write for
# you: the disks, their UUIDs, the filesystems, the kernel modules the initrd
# needs to find them.
#
#   sudo nixos-generate-config --show-hardware-config > hardware-configuration.nix
#
# This throw is deliberate. An empty stub would evaluate, build, and boot into
# a machine with no root filesystem, which fails much later and much less
# clearly than a sentence does.
throw ''
  templates/host: hardware-configuration.nix has not been generated yet.
  Run: sudo nixos-generate-config --show-hardware-config > hardware-configuration.nix
''
