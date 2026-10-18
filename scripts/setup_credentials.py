#!/usr/bin/env python3
"""
Polymarket API Credentials Setup Script

This script reads your wallet private key from config.yml, generates or
derives API credentials from Polymarket CLOB, works out which wallet holds
your funds (api.funder_address and api.signature_type), and writes all of
it into config.yml.

Usage:
    python scripts/setup_credentials.py
    python scripts/setup_credentials.py --wallet 0xYourPolymarketWallet

Requirements:
    pip install -r requirements.txt
"""

import argparse
import json
import os
import sys
import urllib.parse
import urllib.request
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
abi_encode = _import_or_help("eth_abi", "eth-abi", ["encode"]).encode
_eth_utils = _import_or_help("eth_utils", "eth-utils", ["keccak", "to_checksum_address"])
keccak, to_checksum_address = _eth_utils.keccak, _eth_utils.to_checksum_address

# Configuration
HOST = "https://clob.polymarket.com"
CHAIN_ID = 137  # Polygon mainnet
CONFIG_PATH = Path(__file__).parent.parent / "config.yml"
DATA_API = "https://data-api.polymarket.com"


# --- Wallet detection --------------------------------------------------------
#
# A Polymarket account's funds usually sit in a smart wallet controlled by
# your key rather than in the key's own address. Each wallet type's address
# is derived deterministically from the key's address (CREATE2), so we can
# compute every candidate and check which one you use. Constants and formulas
# are ported from Polymarket's official Python SDK (polymarket-client:
# polymarket/_internal/wallet.py and environments.py, Polygon mainnet).

PROXY_FACTORY = "0xaB45c5A4B0c941a2F231C04C3f49182e1A254052"
PROXY_IMPLEMENTATION = "0x44e999d5c2F66Ef0861317f9A4805AC2e90aEB4f"
SAFE_FACTORY = "0xaacFeEa03eb1561C4e67d661e40682Bd20E3541b"
SAFE_INIT_CODE_HASH = "2bce2127ff07fb632d16c8347c4ebf501f4841168bed00d9e6ef715ddb6fcecf"
DEPOSIT_WALLET_FACTORY = "0x00000000000Fb5C9ADea0298D729A0CB3823Cc07"
DEPOSIT_WALLET_IMPLEMENTATION = "0x58CA52ebe0DadfdF531Cde7062e76746de4Db1eB"
DEPOSIT_WALLET_BEACON = "0x7A18EDfe055488A3128f01F563e5B479D92ffc3a"

_PROXY_BYTECODE = (
    "3d3d606380380380913d393d73{factory}"
    "5af4602a57600080fd5b602d8060366000396000f3363d3d373d3d3d363d73{impl}"
    "5af43d82803e903d91602b57fd5bf352e831dd"
    "0000000000000000000000000000000000000000000000000000000000000020"
    "0000000000000000000000000000000000000000000000000000000000000000"
)
_ERC1967_CONST1 = bytes.fromhex("cc3735a920a3ca505d382bbc545af43d6000803e6038573d6000fd5b3d6000f3")
_ERC1967_CONST2 = bytes.fromhex("5155f3363d3d373d3d363d7f360894a13ba1a3210667c828492db98dca3e2076")
_ERC1967_PREFIX = 0x61003D3D8160233D3973
_BEACON_CONST1 = bytes.fromhex("b3582b35133d50545afa5036515af43d6000803e604d573d6000fd5b3d6000f3")
_BEACON_CONST2 = bytes.fromhex("1b60e01b36527fa3f0ad74e5423aebfd80d3ef4346578335a9a72aeaee59ff6c")
_BEACON_CONST3 = bytes.fromhex("60195155f3363d3d373d3d363d602036600436635c60da")
_BEACON_PREFIX = 0x6100523D8160233D3973

# api.signature_type values
EOA, POLY_PROXY, GNOSIS_SAFE, DEPOSIT_WALLET = 0, 1, 2, 3
WALLET_NAMES = {
    EOA: "the key's own address (EOA)",
    POLY_PROXY: "proxy wallet (email / Google login)",
    GNOSIS_SAFE: "Safe wallet (browser wallet login)",
    DEPOSIT_WALLET: "deposit wallet (accounts created from May 2026)",
}


def _bytes(address):
    return bytes.fromhex(address[2:] if address.lower().startswith("0x") else address)


def _create2(factory, salt, bytecode_hash):
    return to_checksum_address(keccak(b"\xff" + _bytes(factory) + salt + bytecode_hash)[12:])


def _proxy_wallet(signer):
    code = _PROXY_BYTECODE.format(factory=PROXY_FACTORY[2:].lower(), impl=PROXY_IMPLEMENTATION[2:].lower())
    return _create2(PROXY_FACTORY, keccak(_bytes(signer)), keccak(bytes.fromhex(code)))


def _safe_wallet(signer):
    salt = keccak(abi_encode(["address"], [signer]))
    return _create2(SAFE_FACTORY, salt, bytes.fromhex(SAFE_INIT_CODE_HASH))


def _deposit_args(signer):
    return abi_encode(["address", "bytes32"], [DEPOSIT_WALLET_FACTORY, _bytes(signer).rjust(32, b"\x00")])


def _deposit_wallet_uups(signer):
    args = _deposit_args(signer)
    prefix = (_ERC1967_PREFIX + (len(args) << 56)).to_bytes(10, "big")
    code = prefix + _bytes(DEPOSIT_WALLET_IMPLEMENTATION) + bytes.fromhex("6009") + _ERC1967_CONST2 + _ERC1967_CONST1 + args
    return _create2(DEPOSIT_WALLET_FACTORY, keccak(args), keccak(code))


def _deposit_wallet_beacon(signer):
    args = _deposit_args(signer)
    prefix = (_BEACON_PREFIX + (len(args) << 56)).to_bytes(10, "big")
    code = prefix + _bytes(DEPOSIT_WALLET_BEACON) + _BEACON_CONST3 + _BEACON_CONST2 + _BEACON_CONST1 + args
    return _create2(DEPOSIT_WALLET_FACTORY, keccak(args), keccak(code))


def wallet_candidates(signer):
    """Every wallet the key can control, as (signature_type, address)."""
    signer = to_checksum_address(signer)
    return [
        (EOA, signer),
        (DEPOSIT_WALLET, _deposit_wallet_beacon(signer)),
        (DEPOSIT_WALLET, _deposit_wallet_uups(signer)),
        (POLY_PROXY, _proxy_wallet(signer)),
        (GNOSIS_SAFE, _safe_wallet(signer)),
    ]


def classify_wallet(signer, wallet):
    """Return the signature_type of a wallet this key controls, or None."""
    wallet = to_checksum_address(wallet)
    for sig_type, address in wallet_candidates(signer):
        if address == wallet:
            return sig_type
    return None


def wallet_has_activity(address):
    """True if Polymarket's Data API has any trades, transfers or yield for the address."""
    query = urllib.parse.urlencode({"user": address, "limit": 1, "exclude_deposits_withdrawals": "false"})
    req = urllib.request.Request(f"{DATA_API}/v2/activity?{query}", headers={"User-Agent": "polymarket-bot-setup"})
    with urllib.request.urlopen(req, timeout=15) as resp:
        return bool(json.load(resp).get("data"))


def resolve_wallet(signer, wallet=None):
    """
    Work out (signature_type, funder_address) for this key. Given a wallet,
    check the key controls it; otherwise find the candidate that has
    activity on Polymarket.
    """
    if wallet:
        try:
            sig_type = classify_wallet(signer, wallet)
        except ValueError:
            print(f"Error: {wallet} is not a valid address")
            sys.exit(1)
        if sig_type is None:
            print(f"Error: {wallet} isn't a wallet this private key owns.")
            print("Check that api.private_key belongs to the same Polymarket account. Session keys")
            print("(separate, time-limited signers for a deposit wallet) aren't supported by the bot.")
            sys.exit(1)
        return sig_type, to_checksum_address(wallet)

    print("Looking for your Polymarket wallet...")
    used = []
    for sig_type, address in wallet_candidates(signer):
        try:
            active = wallet_has_activity(address)
        except Exception as e:
            print(f"Error: couldn't reach the Polymarket Data API - {e}")
            print("Re-run with --wallet <your Polymarket wallet address> to skip the lookup.")
            sys.exit(1)
        print(f"  {'found' if active else 'empty'}  {address}  {WALLET_NAMES[sig_type]}")
        if active:
            used.append((sig_type, address))

    if len(used) == 1:
        return used[0]
    if used:
        print("More than one of these wallets has activity, so I can't tell which one to trade from.")
    else:
        print("None of the wallets this key controls has any activity on Polymarket yet.")
    print("Copy your wallet address from the profile menu on polymarket.com and run:")
    print("  python scripts/setup_credentials.py --wallet 0xYourWalletAddress")
    sys.exit(1)


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


def normalize_private_key(private_key):
    """
    Return the private key as a 0x-prefixed hex string, whichever way it was written.

    Accepts keys with or without 0x/0X, with surrounding whitespace or quotes.
    An unquoted 0x... key in YAML is parsed as an int, so that's handled too.
    """
    if isinstance(private_key, int):
        return '0x' + format(private_key, '064x')

    key = str(private_key).strip().strip('"\'').strip()
    if key[:2].lower() == '0x':
        key = key[2:]
    return '0x' + key


def generate_api_credentials(private_key):
    """
    Generate API credentials from private key using Polymarket CLOB client

    Args:
        private_key: EOA wallet private key (with or without 0x prefix)

    Returns:
        dict: API credentials containing apiKey, secret, and passphrase
    """
    # eth_account and the CLOB client expect a 0x-prefixed key
    private_key = normalize_private_key(private_key)

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
    parser = argparse.ArgumentParser(description="Set up Polymarket credentials in config.yml")
    parser.add_argument("--wallet", help="your Polymarket wallet address (profile menu on polymarket.com); "
                                         "found automatically if omitted")
    args = parser.parse_args()

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
        print("Please add your wallet's private key to the 'api.private_key' field")
        sys.exit(1)

    print("✓ Private key found in config")
    print()

    # Find the wallet that holds the funds before touching anything
    signer = Account.from_key(normalize_private_key(private_key)).address
    wallet = args.wallet or config.get('api', {}).get('funder_address') or None
    sig_type, funder = resolve_wallet(signer, wallet)
    print(f"✓ Wallet: {funder} - {WALLET_NAMES[sig_type]} (signature_type {sig_type})")
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
    config['api']['signature_type'] = sig_type
    config['api']['funder_address'] = "" if sig_type == EOA else funder

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
