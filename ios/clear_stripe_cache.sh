#!/usr/bin/env bash
# Run this once after switching between Xcode 16 and Xcode 26 to fix
# "Unable to resolve Swift module dependency" errors on Stripe targets.
set -e

DERIVED="$HOME/Library/Developer/Xcode/DerivedData"

echo "Clearing stale Stripe module caches from $DERIVED ..."
find "$DERIVED" -name "*.swiftmodule" -path "*Stripe*" -exec rm -rf {} + 2>/dev/null || true
find "$DERIVED" -name "*.swiftsourceinfo" -path "*Stripe*" -delete 2>/dev/null || true
echo "Done. Open Xcode and build normally."
