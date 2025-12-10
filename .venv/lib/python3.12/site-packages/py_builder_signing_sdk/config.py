from typing import Optional, Dict

from .signer import BuilderSigner
from .http_helpers.helpers import post
from .sdk_types import (
    BuilderApiKeyCreds,
    BuilderType,
    BuilderHeaderPayload,
    RemoteBuilderConfig,
)


class BuilderConfig:
    """
    Configuration handler for builder signing/authentication.

    Supports both local signing (via API key credentials) and remote signing (via a remote builder URL + token).
    """

    def __init__(
        self,
        *,
        remote_builder_config: Optional[RemoteBuilderConfig] = None,
        local_builder_creds: Optional[BuilderApiKeyCreds] = None,
    ) -> None:
        self.remote_builder_config: Optional[RemoteBuilderConfig] = None
        self.local_builder_creds: Optional[BuilderApiKeyCreds] = None
        self.signer: Optional[BuilderSigner] = None

        if remote_builder_config is not None:
            if not self._has_valid_remote_url(remote_builder_config.url):
                raise ValueError("invalid remote url!")
            if (
                remote_builder_config.token is not None
                and len(remote_builder_config.token) == 0
            ):
                raise ValueError("invalid auth token")
            self.remote_builder_config = remote_builder_config

        if local_builder_creds is not None:
            if not self._has_valid_local_creds(local_builder_creds):
                raise ValueError("invalid local builder credentials!")
            self.local_builder_creds = local_builder_creds
            self.signer = BuilderSigner(local_builder_creds)

    def generate_builder_headers(
        self,
        method: str,
        path: str,
        body: Optional[str] = None,
        timestamp: Optional[int] = None,
    ) -> Optional[BuilderHeaderPayload]:
        """
        Generate signed builder headers using either local or remote credentials.
        """
        self._ensure_valid()
        builder_type = self.get_builder_type()

        if builder_type == BuilderType.LOCAL:
            return self.signer.create_builder_header_payload(
                method, path, body, timestamp
            )

        if builder_type == BuilderType.REMOTE:
            url = self.remote_builder_config.url
            payload = {
                "method": method,
                "path": path,
                "body": body,
                "timestamp": timestamp,
            }
            try:
                token = self.remote_builder_config.token
                headers = {"Authorization": f"Bearer {token}"} if token else {}
                return post(url, data=payload, headers=headers)
            except Exception as err:
                print("error calling remote signer:", err)
                return None

        return None

    def is_valid(self) -> bool:
        return self.get_builder_type() != BuilderType.UNAVAILABLE

    def get_builder_type(self) -> BuilderType:
        if self.local_builder_creds:
            return BuilderType.LOCAL
        if self.remote_builder_config:
            return BuilderType.REMOTE
        return BuilderType.UNAVAILABLE

    @staticmethod
    def _has_valid_local_creds(creds: Optional[BuilderApiKeyCreds]) -> bool:
        if creds is None:
            return False

        if not (creds.key.strip()):
            return False
        if not (creds.secret.strip()):
            return False
        if not (creds.passphrase.strip()):
            return False
        return True

    @staticmethod
    def _has_valid_remote_url(url: Optional[str]) -> bool:
        return bool(
            url
            and url.strip()
            and (url.startswith("http://") or url.startswith("https://"))
        )

    def _ensure_valid(self) -> None:
        if self.get_builder_type() == BuilderType.UNAVAILABLE:
            raise ValueError("invalid builder creds configured!")
