#!/bin/bash

###############################################################################
# Nyyu Market - SSL Setup Script (Standalone)
# Sets up Let's Encrypt SSL certificates for Nginx
###############################################################################

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

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

check_nginx() {
    if ! command -v nginx &> /dev/null; then
        print_error "Nginx is not installed. Please run setup-nginx.sh first"
        exit 1
    fi

    if ! systemctl is-active --quiet nginx; then
        print_error "Nginx is not running"
        exit 1
    fi
}

install_certbot() {
    print_header "Installing Certbot"

    if command -v certbot &> /dev/null; then
        print_success "Certbot is already installed"
        certbot --version
    else
        apt update
        apt install -y certbot python3-certbot-nginx
        print_success "Certbot installed successfully"
    fi
}

get_domains() {
    print_header "Domain Configuration"

    # Try to extract domains from Nginx config
    if [[ -f /etc/nginx/sites-available/nyyu-market ]]; then
        HTTP_DOMAIN=$(grep "server_name" /etc/nginx/sites-available/nyyu-market | grep -v "_" | grep -v "#" | head -1 | awk '{print $2}' | tr -d ';')
        GRPC_DOMAIN=$(grep "server_name" /etc/nginx/sites-available/nyyu-market | grep -v "_" | grep -v "#" | tail -1 | awk '{print $2}' | tr -d ';')

        if [[ -n "$HTTP_DOMAIN" ]] && [[ "$HTTP_DOMAIN" != "api.yourdomain.com" ]]; then
            print_info "Detected domains from Nginx config:"
            echo "  HTTP API: $HTTP_DOMAIN"
            echo "  gRPC API: $GRPC_DOMAIN"
            echo ""
            read -p "Use these domains? (Y/n): " -n 1 -r
            echo
            if [[ $REPLY =~ ^[Nn]$ ]]; then
                HTTP_DOMAIN=""
                GRPC_DOMAIN=""
            fi
        fi
    fi

    # Ask user if not detected
    if [[ -z "$HTTP_DOMAIN" ]]; then
        read -p "Enter your HTTP API domain (e.g., api.yourdomain.com): " HTTP_DOMAIN
    fi

    if [[ -z "$GRPC_DOMAIN" ]]; then
        read -p "Enter your gRPC API domain (e.g., grpc.yourdomain.com): " GRPC_DOMAIN
    fi

    read -p "Enter email for SSL certificate notifications: " EMAIL

    if [[ -z "$HTTP_DOMAIN" ]] || [[ -z "$GRPC_DOMAIN" ]] || [[ -z "$EMAIL" ]]; then
        print_error "Domain names and email are required"
        exit 1
    fi
}

check_dns() {
    print_header "Checking DNS Configuration"

    print_info "Checking $HTTP_DOMAIN..."
    if host "$HTTP_DOMAIN" > /dev/null 2>&1; then
        IP=$(host "$HTTP_DOMAIN" | grep "has address" | awk '{print $4}' | head -1)
        print_success "$HTTP_DOMAIN resolves to $IP"
    else
        print_warning "$HTTP_DOMAIN does not resolve"
        print_warning "Make sure your domain points to this server!"
    fi

    print_info "Checking $GRPC_DOMAIN..."
    if host "$GRPC_DOMAIN" > /dev/null 2>&1; then
        IP=$(host "$GRPC_DOMAIN" | grep "has address" | awk '{print $4}' | head -1)
        print_success "$GRPC_DOMAIN resolves to $IP"
    else
        print_warning "$GRPC_DOMAIN does not resolve"
        print_warning "Make sure your domain points to this server!"
    fi

    echo ""
    print_warning "Prerequisites for SSL:"
    echo "  ✓ Domains must point to this server's IP"
    echo "  ✓ Port 80 must be open and accessible"
    echo "  ✓ Port 443 must be open and accessible"
    echo "  ✓ No other service using port 80"
    echo ""

    read -p "Continue with SSL setup? (y/N): " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        print_info "Cancelled. Fix DNS and firewall, then run this script again."
        exit 0
    fi
}

obtain_certificates() {
    print_header "Obtaining SSL Certificates"

    print_info "Requesting certificates from Let's Encrypt..."
    echo ""

    # Get certificates for both domains
    if certbot --nginx \
        -d "$HTTP_DOMAIN" \
        -d "$GRPC_DOMAIN" \
        --non-interactive \
        --agree-tos \
        -m "$EMAIL" \
        --redirect; then

        print_success "SSL certificates obtained successfully!"
    else
        print_error "Failed to obtain SSL certificates"
        echo ""
        print_info "Common issues:"
        echo "  1. Domains not pointing to this server"
        echo "  2. Firewall blocking port 80 or 443"
        echo "  3. Another service using port 80"
        echo "  4. Nginx configuration errors"
        echo ""
        print_info "Check logs: sudo journalctl -u certbot"
        exit 1
    fi
}

configure_auto_renewal() {
    print_header "Configuring Auto-Renewal"

    # Certbot installs a systemd timer for auto-renewal
    systemctl enable certbot.timer
    systemctl start certbot.timer

    print_success "Auto-renewal configured"
    print_info "Certificates will automatically renew before expiration"

    # Test renewal
    print_info "Testing renewal process (dry run)..."
    if certbot renew --dry-run; then
        print_success "Renewal test passed"
    else
        print_warning "Renewal test failed (but certificates are installed)"
    fi

    # Show timer status
    echo ""
    print_info "Certbot timer status:"
    systemctl status certbot.timer --no-pager | grep -E "Active|Trigger"
}

update_nginx_config() {
    print_header "Updating Nginx Configuration"

    # Uncomment SSL directives if they exist
    if [[ -f /etc/nginx/sites-available/nyyu-market ]]; then
        sed -i 's/# ssl_certificate/ssl_certificate/g' /etc/nginx/sites-available/nyyu-market
        sed -i 's/# return 301/return 301/g' /etc/nginx/sites-available/nyyu-market

        print_success "Nginx configuration updated"
    fi

    # Test and reload
    if nginx -t; then
        systemctl reload nginx
        print_success "Nginx reloaded with SSL configuration"
    else
        print_error "Nginx configuration test failed"
        exit 1
    fi
}

test_ssl() {
    print_header "Testing SSL Configuration"

    print_info "Testing HTTPS endpoints..."

    sleep 2

    # Test HTTP API
    if curl -s -I "https://$HTTP_DOMAIN" > /dev/null 2>&1; then
        print_success "HTTPS HTTP API is accessible"
    else
        print_warning "HTTPS HTTP API test failed"
    fi

    # Check certificate info
    print_info "Certificate information:"
    echo | openssl s_client -connect "$HTTP_DOMAIN:443" -servername "$HTTP_DOMAIN" 2>/dev/null | openssl x509 -noout -dates

    echo ""
    print_info "SSL Labs Test:"
    echo "  Visit: https://www.ssllabs.com/ssltest/analyze.html?d=$HTTP_DOMAIN"
}

print_summary() {
    print_header "SSL Setup Complete! 🎉"

    echo ""
    echo -e "${GREEN}SSL certificates have been installed successfully!${NC}"
    echo ""

    echo -e "${BLUE}=== Secure Access URLs ===${NC}"
    echo "HTTP API:  https://$HTTP_DOMAIN"
    echo "gRPC API:  grpc://$GRPC_DOMAIN:443 (with TLS)"
    echo ""

    echo -e "${BLUE}=== Certificate Information ===${NC}"
    echo "Certificates: /etc/letsencrypt/live/$HTTP_DOMAIN/"
    echo "Auto-renewal: Enabled (checks twice daily)"
    echo "Valid for:    90 days (auto-renews at 30 days)"
    echo ""

    echo -e "${BLUE}=== Test Your SSL ===${NC}"
    echo "Browser:     https://$HTTP_DOMAIN/health"
    echo "SSL Labs:    https://www.ssllabs.com/ssltest/analyze.html?d=$HTTP_DOMAIN"
    echo "Certificate: openssl s_client -connect $HTTP_DOMAIN:443"
    echo ""

    echo -e "${BLUE}=== Update Postman for gRPC ===${NC}"
    echo "1. In Postman, change URL to: grpc://$GRPC_DOMAIN:443"
    echo "2. Enable 'Use TLS connection'"
    echo "3. Test your gRPC methods"
    echo ""

    echo -e "${BLUE}=== Certificate Management ===${NC}"
    echo "List certificates:  sudo certbot certificates"
    echo "Renew manually:     sudo certbot renew"
    echo "Test renewal:       sudo certbot renew --dry-run"
    echo "Revoke certificate: sudo certbot revoke --cert-path /etc/letsencrypt/live/$HTTP_DOMAIN/cert.pem"
    echo ""

    echo -e "${GREEN}Your service is now secured with HTTPS/TLS! 🔒${NC}"
    echo ""
}

main() {
    print_header "Nyyu Market - SSL Setup with Let's Encrypt"

    check_root
    check_nginx
    install_certbot
    get_domains
    check_dns
    obtain_certificates
    configure_auto_renewal
    update_nginx_config
    test_ssl
    print_summary
}

# Run main
if [[ $EUID -eq 0 ]]; then
    main "$@"
else
    print_error "This script must be run with sudo"
    echo "Usage: sudo bash setup-ssl.sh"
    exit 1
fi
