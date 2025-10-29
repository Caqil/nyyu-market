# Nyyu Market - GitHub Actions Deployment

## Automated Build & Deploy with GitHub Actions

This project uses GitHub Actions to automatically build Docker images and deploy to your Azure server.

---

## Setup (One-Time)

### 1. Configure GitHub Repository Secrets

Go to your GitHub repository: **Settings → Secrets and variables → Actions → New repository secret**

Add these secrets:

| Secret Name | Value |
|-------------|-------|
| `AZURE_HOST` | `market.nyyu.io` or `nyyu-market.uksouth.cloudapp.azure.com` |
| `AZURE_USERNAME` | `azureuser` |
| `AZURE_SSH_KEY` | Your private SSH key (the full content from Azure-NYYU.pem) |

**For AZURE_SSH_KEY:**
- Copy the entire content of your `.pem` file including:
  ```
  -----BEGIN RSA PRIVATE KEY-----
  ...your key content...
  -----END RSA PRIVATE KEY-----
  ```

### 2. Enable GitHub Container Registry

The workflow automatically publishes images to GitHub Container Registry (ghcr.io). No additional setup needed - it uses `GITHUB_TOKEN` automatically.

### 3. Prepare Azure Server (One-Time)

SSH into your Azure server:

```bash
ssh -i Azure-NYYU.pem azureuser@market.nyyu.io
```

Then run:

```bash
# Install Docker
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker azureuser

# Install Docker Compose
sudo apt update
sudo apt install -y docker-compose-plugin

# Clone repository
cd /opt
sudo git clone https://github.com/Caqil/nyyu-market.git
sudo chown -R azureuser:azureuser nyyu-market
cd nyyu-market

# Create .env file (copy passwords from install logs)
nano .env

# Install and configure Nginx
sudo apt install -y nginx
sudo nano /etc/nginx/sites-available/nyyu-market
```

**Nginx configuration for market.nyyu.io:**

```nginx
upstream nyyu_http_api {
    server localhost:8080;
    keepalive 32;
}

upstream nyyu_grpc_api {
    server localhost:50051;
    keepalive 32;
}

server {
    listen 80;
    server_name market.nyyu.io;

    location / {
        proxy_pass http://nyyu_http_api;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Connection "";
    }

    location /health {
        proxy_pass http://nyyu_http_api/health;
        access_log off;
    }
}
```

Enable and test:

```bash
sudo ln -s /etc/nginx/sites-available/nyyu-market /etc/nginx/sites-enabled/
sudo rm -f /etc/nginx/sites-enabled/default
sudo nginx -t
sudo systemctl restart nginx
```

**Setup SSL with Let's Encrypt:**

```bash
sudo apt install -y certbot python3-certbot-nginx
sudo certbot --nginx -d market.nyyu.io --non-interactive --agree-tos -m your-email@example.com
```

**Setup Firewall:**

```bash
sudo ufw allow 22/tcp
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw --force enable
```

---

## How It Works

### Automatic Deployment

Every time you push to `main` branch:

1. ✅ GitHub Actions builds the Docker image
2. ✅ Pushes image to GitHub Container Registry
3. ✅ SSHs into your Azure server
4. ✅ Pulls the latest image
5. ✅ Restarts the containers with zero downtime

**Workflow file:** `.github/workflows/docker-build.yml`

### Manual Deployment

You can also trigger deployment manually:

1. Go to **Actions** tab in GitHub
2. Click **Build, Push and Deploy** workflow
3. Click **Run workflow** → **Run workflow**

---

## Deployment

### Push to Deploy

Just commit and push your changes:

```bash
git add .
git commit -m "Your changes"
git push
```

GitHub Actions will automatically:
- Build the Docker image
- Deploy to your Azure server
- Show the deployment status in Actions tab

### Check Deployment Status

Go to your repository's **Actions** tab to see:
- ✅ Build progress
- ✅ Deployment logs
- ✅ Any errors

---

## Verify Deployment

After deployment completes:

**Test HTTP API:**
```bash
curl https://market.nyyu.io/health
curl https://market.nyyu.io/api/v1/stats
```

**Test gRPC API (Postman):**
- URL: `market.nyyu.io:443`
- Enable TLS
- Import: `proto/market.proto`

**Check logs on server:**
```bash
ssh -i Azure-NYYU.pem azureuser@market.nyyu.io
cd /opt/nyyu-market
docker compose -f docker-compose.prod.yml logs -f
```

---

## Server Management

### View Logs
```bash
cd /opt/nyyu-market
docker compose -f docker-compose.prod.yml logs -f nyyu-market
```

### Restart Services
```bash
docker compose -f docker-compose.prod.yml restart
```

### Stop Services
```bash
docker compose -f docker-compose.prod.yml down
```

### Start Services
```bash
docker compose -f docker-compose.prod.yml up -d
```

### Check Status
```bash
docker compose -f docker-compose.prod.yml ps
docker compose -f docker-compose.prod.yml exec nyyu-market wget -qO- http://localhost:8080/health
```

---

## Troubleshooting

### Build Failed in GitHub Actions

Check the Actions tab for error logs. Common issues:
- **Go compilation errors:** Fix code and push again
- **Docker build timeout:** Should work now since build is on GitHub's servers

### Deployment Failed

Check GitHub Actions logs. Common issues:
- **SSH connection failed:** Verify `AZURE_SSH_KEY` secret is correct
- **Docker pull failed:** Make sure the image was built successfully
- **Permission denied:** Check that azureuser has docker permissions

### Service Not Starting

SSH into server and check logs:
```bash
cd /opt/nyyu-market
docker compose -f docker-compose.prod.yml logs nyyu-market
```

### SSL Certificate Issues

Renew certificate:
```bash
sudo certbot renew
sudo systemctl reload nginx
```

---

## Architecture

```
GitHub Push → GitHub Actions → Build Docker Image → Push to GHCR
                                                          ↓
                                    SSH Deploy → Azure Server
                                                          ↓
                                Pull Image → Restart Containers → Production
```

**Benefits:**
- ✅ No building on your Azure server (fast & reliable)
- ✅ Automatic deployment on every push
- ✅ Build caching for faster builds
- ✅ Zero-downtime deployments
- ✅ Easy rollback (just redeploy previous commit)

---

## URLs

- **HTTP API:** https://market.nyyu.io
- **gRPC API:** market.nyyu.io:443 (with TLS)
- **Health Check:** https://market.nyyu.io/health
- **Stats:** https://market.nyyu.io/api/v1/stats
- **Docker Image:** ghcr.io/caqil/nyyu-market:latest

---

## Next Steps

1. ✅ Set up GitHub secrets
2. ✅ Prepare Azure server (install Docker, Nginx, SSL)
3. ✅ Push to main branch
4. ✅ Watch Actions tab for deployment progress
5. ✅ Test your API at https://market.nyyu.io

**That's it! Every push will now automatically deploy to production.** 🚀
