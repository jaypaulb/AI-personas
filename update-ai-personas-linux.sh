#!/bin/bash
# Update and restart ai-personas-linux using pm2
# This script discards all local changes and gets the latest from git
# Note: The binary must be pre-built and committed to git

set -e

# Discard all local changes and reset to match remote
git fetch origin
git reset --hard origin/master

# Pull latest changes (should be up to date after reset, but just in case)
git pull

# Make binary executable (it should already exist from git)
chmod +x ai-personas-linux

# Restart with pm2
pm2 restart ai-personas-linux || pm2 start ./ai-personas-linux --name ai-personas-linux