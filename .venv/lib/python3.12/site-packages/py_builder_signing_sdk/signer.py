from typing import Optional
import time

from .sdk_types import BuilderApiKeyCreds, BuilderHeaderPayload
from .signing.hmac import build_hmac_signature


class BuilderSigner:
    def __init__(self, creds: BuilderApiKeyCreds):
        self.creds = creds

    def create_builder_header_payload(
        self,
        method: str,
        path: str,
        body: Optional[str] = None,
        timestamp: Optional[int] = None,
    ) -> BuilderHeaderPayload:
        """
        Creates a builder header payload

        Args:
            method: HTTP method
            path: Request path
            body: Optional request body
            timestamp: Optional timestamp (defaults to current time)

        Returns:
            Builder header payload
        """
        ts = int(time.time())
        if timestamp is not None:
            ts = timestamp

        builder_sig = build_hmac_signature(
            self.creds.secret,
            str(ts),
            method,
            path,
            body,
        )

        return BuilderHeaderPayload(
            POLY_BUILDER_API_KEY=self.creds.key,
            POLY_BUILDER_PASSPHRASE=self.creds.passphrase,
            POLY_BUILDER_SIGNATURE=builder_sig,
            POLY_BUILDER_TIMESTAMP=str(ts),
        )
