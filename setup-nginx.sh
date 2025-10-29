#!/bin/bash

###############################################################################
# Nyyu Market - Nginx Setup Script
# Installs and configures Nginx as reverse proxy
###############################################################################

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Script directory
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

print_header() {
    echo ""
    echo -e "${BLUE}================================================${NC}"
    echo -e "${BLUE}  $1${NC}"
    echo -e "${BLUE}================================================${NC}"
    echo ""
}

print_success() {
    echo -e "${GREEN}✅ $1${NC}"
}

print_error() {
    echo -e "${RED}❌ $1${NC}"
}

print_warning() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

print_info() {
    echo -e "${BLUE}ℹ️  $1${NC}"
}

check_root() {
    if [[ $EUID -ne 0 ]]; then
        print_error "This script must be run as root or with sudo"
        exit 1
    fi
}

install_nginx() {
    print_header "Installing Nginx"

    if command -v nginx &> /dev/null; then
        print_warning "Nginx is already installed"
        nginx -v
        read -p "Do you want to continue and reconfigure? (y/N): " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            exit 0
        fi
    else
        apt update
        apt install -y nginx
        print_success "Nginx installed successfully"
    fi

    # Enable and start Nginx
    systemctl enable nginx
    systemctl start nginx

    nginx -v
}

choose_config_type() {
    print_header "Choose Nginx Configuration Type"

    echo "1. Production (with domain and SSL)"
    echo "2. Local/Development (without SSL)"
    echo ""
    read -p "Select option [1-2]: " -n 1 -r
    echo ""

    case $REPLY in
        1)
            CONFIG_TYPE="production"
            setup_production_config
            ;;
        2)
            CONFIG_TYPE="local"
            setup_local_config
            ;;
        *)
            print_error "Invalid option"
            exit 1
            ;;
    esac
}

setup_local_config() {
    print_header "Setting Up Local Nginx Configuration"

    # Copy local config
    cp "$SCRIPT_DIR/nginx/nginx-local.conf" /etc/nginx/sites-available/nyyu-market

    # Create symlink
    ln -sf /etc/nginx/sites-available/nyyu-market /etc/nginx/sites-enabled/nyyu-market

    # Remove default site
    rm -f /etc/nginx/sites-enabled/default

    # Test config
    nginx -t

    # Reload Nginx
    systemctl reload nginx

    print_success "Nginx configured for local development"
    echo ""
    print_info "Access your service at:"
    echo "  HTTP API:  http://localhost (port 80)"
    echo "  gRPC API:  localhost:9443 (HTTP/2 without SSL)"
    echo ""
    print_warning "Note: This configuration is for development only"
}

setup_production_config() {
    print_header "Setting Up Production Nginx Configuration"

    # Ask for domain names
    read -p "Enter your HTTP API domain (e.g., api.yourdomain.com): " HTTP_DOMAIN
    read -p "Enter your gRPC API domain (e.g., grpc.yourdomain.com): " GRPC_DOMAIN

    if [[ -z "$HTTP_DOMAIN" ]] || [[ -z "$GRPC_DOMAIN" ]]; then
        print_error "Domain names are required for production setup"
        exit 1
    fi

    # Copy config and replace domains
    cp "$SCRIPT_DIR/nginx/nginx.conf" /etc/nginx/sites-available/nyyu-market
    sed -i "s/api.yourdomain.com/$HTTP_DOMAIN/g" /etc/nginx/sites-available/nyyu-market
    sed -i "s/grpc.yourdomain.com/$GRPC_DOMAIN/g" /etc/nginx/sites-available/nyyu-market

    # Create symlink
    ln -sf /etc/nginx/sites-available/nyyu-market /etc/nginx/sites-enabled/nyyu-market

    # Remove default site
    rm -f /etc/nginx/sites-enabled/default

    # Test config
    nginx -t

    # Reload Nginx
    systemctl reload nginx

    print_success "Nginx configured for production"
    echo ""
    print_warning "Next steps:"
    echo "1. Make sure your domains point to this server's IP"
    echo "2. Run SSL setup: sudo bash setup-ssl.sh"
    echo ""
    print_info "Temporary access (HTTP only):"
    echo "  HTTP API:  http://$HTTP_DOMAIN"
}

setup_ssl() {
    print_header "SSL Certificate Setup"

    if [[ "$CONFIG_TYPE" != "production" ]]; then
        print_warning "SSL is only for production configuration"
        return
    fi

    print_info "Installing Certbot..."
    apt install -y certbot python3-certbot-nginx

    read -p "Enter email for SSL certificate notifications: " EMAIL

    if [[ -z "$EMAIL" ]]; then
        print_error "Email is required for SSL setup"
        exit 1
    fi

    print_info "Obtaining SSL certificates..."
    print_warning "Make sure your domains are pointing to this server!"
    echo ""
    read -p "Continue? (y/N): " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        print_info "Skipping SSL setup. You can run it later with: sudo certbot --nginx"
        return
    fi

    # Get certificates for both domains
    certbot --nginx -d "$HTTP_DOMAIN" -d "$GRPC_DOMAIN" --non-interactive --agree-tos -m "$EMAIL"

    if [[ $? -eq 0 ]]; then
        print_success "SSL certificates installed successfully"

        # Uncomment SSL lines in config
        sed -i 's/# ssl_certificate/ssl_certificate/g' /etc/nginx/sites-available/nyyu-market
        sed -i 's/# return 301/return 301/g' /etc/nginx/sites-available/nyyu-market

        nginx -t
        systemctl reload nginx

        print_success "SSL configured and enabled"
        echo ""
        print_info "Access your service at:"
        echo "  HTTP API:  https://$HTTP_DOMAIN"
        echo "  gRPC API:  grpc://$GRPC_DOMAIN:443 (SSL enabled)"
        echo ""
        print_info "SSL certificates will auto-renew via certbot timer"
        systemctl status certbot.timer --no-pager
    else
        print_error "SSL certificate installation failed"
        print_info "Check that:"
        echo "  1. Domains are pointing to this server"
        echo "  2. Ports 80 and 443 are open"
        echo "  3. No other service is using port 80"
    fi
}

test_nginx() {
    print_header "Testing Nginx Configuration"

    # Test Nginx config
    if nginx -t; then
        print_success "Nginx configuration is valid"
    else
        print_error "Nginx configuration has errors"
        exit 1
    fi

    # Test HTTP endpoint
    print_info "Testing HTTP endpoint..."
    sleep 2

    if curl -s http://localhost/health > /dev/null 2>&1; then
        print_success "HTTP API is accessible through Nginx"
    else
        print_warning "HTTP API test failed (service might not be running yet)"
    fi
}

print_summary() {
    print_header "Nginx Setup Complete! 🎉"

    echo ""
    echo -e "${GREEN}Nginx has been configured successfully!${NC}"
    echo ""

    if [[ "$CONFIG_TYPE" == "local" ]]; then
        echo -e "${BLUE}=== Local Access ===${NC}"
        echo "HTTP API:  http://localhost"
        echo "gRPC API:  localhost:9443"
        echo ""
        echo -e "${YELLOW}Note: This is for development only${NC}"
    else
        echo -e "${BLUE}=== Production Access ===${NC}"
        echo "HTTP API:  http://$HTTP_DOMAIN"
        echo "gRPC API:  grpc://$GRPC_DOMAIN (after SSL setup)"
        echo ""
        if [[ -z "$EMAIL" ]]; then
            echo -e "${YELLOW}SSL not configured yet. Run: sudo bash setup-ssl.sh${NC}"
        else
            echo -e "${GREEN}SSL is configured and active${NC}"
        fi
    fi

    echo ""
    echo -e "${BLUE}=== Nginx Commands ===${NC}"
    echo "Test config:    sudo nginx -t"
    echo "Reload:         sudo systemctl reload nginx"
    echo "Restart:        sudo systemctl restart nginx"
    echo "Status:         sudo systemctl status nginx"
    echo "Access logs:    sudo tail -f /var/log/nginx/nyyu_*_access.log"
    echo "Error logs:     sudo tail -f /var/log/nginx/nyyu_*_error.log"
    echo ""
}

main() {
    print_header "Nyyu Market - Nginx Setup"

    check_root
    install_nginx
    choose_config_type

    if [[ "$CONFIG_TYPE" == "production" ]]; then
        read -p "Do you want to set up SSL now? (y/N): " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            setup_ssl
        else
            print_info "You can set up SSL later with: sudo certbot --nginx"
        fi
    fi

    test_nginx
    print_summary
}

# Run main
if [[ $EUID -eq 0 ]]; then
    main "$@"
else
    print_error "This script must be run with sudo"
    echo "Usage: sudo bash setup-nginx.sh"
    exit 1
fi
