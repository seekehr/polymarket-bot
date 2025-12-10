#!/usr/bin/env python3
"""
Polymarket API Credentials Setup Script

This script reads your EOA wallet private key from config.yml,
generates or derives API credentials from Polymarket CLOB,
and automatically updates config.yml with the credentials.

Usage:
    python scripts/setup_credentials.py

Requirements:
    pip install py-clob-client eth-account pyyaml
"""

import os
import sys
from pathlib import Path

# Defer third-party imports to provide clearer guidance if dependencies
# are not yet installed (common with Debian's externally-managed Python).
def _import_or_help(module_name, package_name, from_list=None):
    """
    Import a module and show actionable install guidance if missing.

    Args:
        module_name: name passed to __import__
        package_name: pip package name to suggest
        from_list: fromlist for __import__
    """
    try:
        return __import__(module_name, fromlist=from_list or [])
    except ImportError:
        print(f"Error: Missing dependency '{package_name}'.")
        print("Create and use a virtual environment to install requirements:")
        print("  python3 -m venv .venv")
        print("  source .venv/bin/activate")
        print("  pip install -r requirements.txt")
        sys.exit(1)


yaml = _import_or_help("yaml", "pyyaml")
ClobClient = _import_or_help("py_clob_client.client", "py-clob-client", ["ClobClient"]).ClobClient
Account = _import_or_help("eth_account", "eth-account", ["Account"]).Account

# Configuration
HOST = "https://clob.polymarket.com"
CHAIN_ID = 137  # Polygon mainnet
CONFIG_PATH = Path(__file__).parent.parent / "config.yml"


def load_config():
    """Load configuration from config.yml"""
    if not CONFIG_PATH.exists():
        print(f"Error: Configuration file not found at {CONFIG_PATH}")
        print("Please copy config.example.yml to config.yml and add your private key")
        sys.exit(1)

    with open(CONFIG_PATH, 'r') as f:
        return yaml.safe_load(f)


def save_config(config):
    """Save configuration back to config.yml"""
    with open(CONFIG_PATH, 'w') as f:
        yaml.dump(config, f, default_flow_style=False, sort_keys=False)


def generate_api_credentials(private_key):
    """
    Generate API credentials from private key using Polymarket CLOB client

    Args:
        private_key: EOA wallet private key (with or without 0x prefix)

    Returns:
        dict: API credentials containing apiKey, secret, and passphrase
    """
    # Ensure private key has 0x prefix for eth_account
    if not private_key.startswith('0x'):
        private_key = '0x' + private_key

    # Create account from private key
    try:
        account = Account.from_key(private_key)
        print(f"✓ Successfully loaded wallet: {account.address}")
    except Exception as e:
        print(f"Error: Invalid private key - {e}")
        sys.exit(1)

    # Create temporary CLOB client
    print("Connecting to Polymarket CLOB...")
    try:
        client = ClobClient(HOST, chain_id=CHAIN_ID, key=private_key)
        print("✓ Connected to Polymarket CLOB")
    except Exception as e:
        print(f"Error: Failed to create CLOB client - {e}")
        sys.exit(1)

    # Generate or derive API credentials
    print("Generating API credentials...")
    try:
        api_creds = client.create_or_derive_api_creds()
        print("✓ Successfully generated API credentials")
        return api_creds
    except Exception as e:
        print(f"Error: Failed to generate API credentials - {e}")
        sys.exit(1)


def main():
    print("=" * 60)
    print("Polymarket API Credentials Setup")
    print("=" * 60)
    print()

    # Load config
    print(f"Loading configuration from {CONFIG_PATH}...")
    config = load_config()

    # Get private key from config
    private_key = config.get('api', {}).get('private_key', '')

    if not private_key or private_key == 'your_private_key_here':
        print("Error: No private key found in config.yml")
        print("Please add your EOA wallet private key to the 'api.private_key' field")
        sys.exit(1)

    print("✓ Private key found in config")
    print()

    # Generate credentials
    api_creds = generate_api_credentials(private_key)
    print()

    # Display credentials (for verification)
    print("Generated credentials:")
    print(f"  API Key: {api_creds.api_key[:10]}...{api_creds.api_key[-10:]}")
    print(f"  Secret: {api_creds.api_secret[:10]}...{api_creds.api_secret[-10:]}")
    print(f"  Passphrase: {api_creds.api_passphrase[:5]}...{api_creds.api_passphrase[-5:]}")
    print()

    # Update config with credentials
    print("Updating config.yml with API credentials...")
    config['api']['api_key'] = api_creds.api_key
    config['api']['api_secret'] = api_creds.api_secret
    config['api']['passphrase'] = api_creds.api_passphrase

    # Save updated config
    save_config(config)
    print(f"✓ Configuration saved to {CONFIG_PATH}")
    print()

    print("=" * 60)
    print("Setup complete!")
    print("=" * 60)
    print()
    print("IMPORTANT SECURITY NOTES:")
    print("1. Your config.yml now contains sensitive API credentials")
    print("2. Never commit config.yml to version control")
    print("3. Ensure config.yml is in your .gitignore")
    print("4. Store backups securely")
    print()
    print("You can now run your trading bot with these credentials.")


if __name__ == "__main__":
    main()
