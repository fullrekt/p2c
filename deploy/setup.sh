#!/bin/bash
# Run once on a fresh VPS to set up the deployment target.
# Usage: bash setup.sh
set -e

APP_DIR="$HOME/p2c-sniper"
REPO_DIR="$HOME/p2c-sniper.git"

echo "=== P2C Sniper Bot — VPS setup ==="

# ── Docker ────────────────────────────────────────────────────────────────────
if ! command -v docker &>/dev/null; then
  echo "Installing Docker..."
  curl -fsSL https://get.docker.com | sh
  sudo usermod -aG docker "$USER"
  echo "Docker installed. Re-login or run: newgrp docker"
else
  echo "Docker already installed: $(docker --version)"
fi

# ── Directories ───────────────────────────────────────────────────────────────
mkdir -p "$APP_DIR/data"
echo "Working directory: $APP_DIR"

# ── Bare git repo ─────────────────────────────────────────────────────────────
if [ -d "$REPO_DIR" ]; then
  echo "Bare repo already exists at $REPO_DIR, skipping git init"
else
  git init --bare "$REPO_DIR"
  echo "Bare repo created: $REPO_DIR"
fi

# ── post-receive hook ─────────────────────────────────────────────────────────
HOOK="$REPO_DIR/hooks/post-receive"

cat > "$HOOK" << 'HOOK_SCRIPT'
#!/bin/bash
set -e

REPO_DIR="$HOME/p2c-sniper.git"
WORK_DIR="$HOME/p2c-sniper"

echo "▶ Deploying P2C Sniper Bot..."

git --work-tree="$WORK_DIR" --git-dir="$REPO_DIR" checkout -f

cd "$WORK_DIR"

if [ ! -f .env ]; then
  echo "✗ .env not found at $WORK_DIR/.env"
  echo "  Create it: nano ~/p2c-sniper/.env"
  exit 1
fi

docker compose build --no-cache
docker compose up -d --force-recreate --remove-orphans

echo "✓ Deploy complete"
docker compose ps
HOOK_SCRIPT

chmod +x "$HOOK"
echo "post-receive hook installed: $HOOK"

# ── .env reminder ─────────────────────────────────────────────────────────────
if [ ! -f "$APP_DIR/.env" ]; then
  echo ""
  echo "⚠ Don't forget to create .env before the first deploy:"
  echo "  nano $APP_DIR/.env"
fi

# ── Summary ───────────────────────────────────────────────────────────────────
echo ""
echo "=== Setup complete ==="
echo ""
echo "On your LOCAL machine run once:"
echo ""
echo "  git init  # if not already a git repo"
echo "  git remote add production ssh://$(whoami)@<YOUR_SERVER_IP>$REPO_DIR"
echo "  git push production main"
echo ""
echo "Every subsequent push to 'production' will auto-deploy."
