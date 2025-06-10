#!/bin/bash
# Update and restart ai-personas-linux using pm2

set -e

# Remove old binary
rm -f ai-personas-linux

# Pull latest changes
git pull

# Make new binary executable
sudo chmod +x ai-personas-linux

# Restart with pm2
pm2 restart ai-personas-linux || pm2 start ./ai-personas-linux --name ai-personas-linux 