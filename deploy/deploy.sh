#!/bin/bash
set -e

# Configuration
DEPLOY_DIR="/opt/deltago"
REPO_URL="https://github.com/${GITHUB_REPOSITORY}.git"
SERVICE_NAME="deltago"

echo "=== DeltaGo Deployment Script ==="

# Create deployment directory
sudo mkdir -p $DEPLOY_DIR
sudo mkdir -p /etc/deltago

# Clone or update repository
if [ -d "$DEPLOY_DIR/.git" ]; then
    echo "Updating existing repository..."
    cd $DEPLOY_DIR
    sudo git fetch origin
    sudo git reset --hard origin/main
else
    echo "Cloning repository..."
    sudo rm -rf $DEPLOY_DIR/*
    sudo git clone $REPO_URL $DEPLOY_DIR
    cd $DEPLOY_DIR
fi

# Build the Go binary
echo "Building Go binary..."
cd $DEPLOY_DIR/cmd/bot
sudo /usr/local/go/bin/go build -o $DEPLOY_DIR/bot .

# Copy config file
sudo cp $DEPLOY_DIR/config.yaml $DEPLOY_DIR/config.yaml

# Write environment variables
echo "Setting up environment variables..."
sudo tee /etc/deltago/env > /dev/null << EOF
DELTA_API_KEY=${DELTA_API_KEY}
DELTA_API_SECRET=${DELTA_API_SECRET}
EOF
sudo chmod 600 /etc/deltago/env

# Install systemd service
echo "Installing systemd service..."
sudo cp $DEPLOY_DIR/deploy/deltago.service /etc/systemd/system/
sudo systemctl daemon-reload

# Stop existing service if running
sudo systemctl stop $SERVICE_NAME 2>/dev/null || true

# Start the service
echo "Starting service..."
sudo systemctl enable $SERVICE_NAME
sudo systemctl start $SERVICE_NAME

# Show status
echo "=== Deployment Complete ==="
sudo systemctl status $SERVICE_NAME --no-pager
