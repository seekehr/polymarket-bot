from unittest import TestCase
import responses

from py_builder_signing_sdk.config import BuilderConfig
from py_builder_signing_sdk.sdk_types import (
    BuilderApiKeyCreds,
    RemoteBuilderConfig,
    BuilderType,
    BuilderHeaderPayload,
)


class TestBuilder(TestCase):
    def test_is_valid(self):
        #  is_valid false
        builder_config = BuilderConfig()
        self.assertFalse(builder_config.is_valid())

        # is_valid true
        builder_config = BuilderConfig(
            local_builder_creds=BuilderApiKeyCreds(
                key="019894b9-cb40-79c4-b2bd-6aecb6f8c6c5",
                secret="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
                passphrase="1816e5ed89518467ffa78c65a2d6a62d240f6fd6d159cba7b2c4dc510800f75a",
            )
        )
        self.assertTrue(builder_config.is_valid())

        # is_valid true
        builder_config = BuilderConfig(
            remote_builder_config=RemoteBuilderConfig(
                url="http://localhost:3000/sign", token="MY_AUTH_TOKEN"
            ),
        )
        self.assertTrue(builder_config.is_valid())

    def test_get_builder_type(self):
        builder_config = BuilderConfig(
            local_builder_creds=BuilderApiKeyCreds(
                key="019894b9-cb40-79c4-b2bd-6aecb6f8c6c5",
                secret="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
                passphrase="1816e5ed89518467ffa78c65a2d6a62d240f6fd6d159cba7b2c4dc510800f75a",
            )
        )
        self.assertEqual(builder_config.get_builder_type(), BuilderType.LOCAL)

        builder_config = BuilderConfig(
            remote_builder_config=RemoteBuilderConfig(
                url="http://localhost:3000/sign",
            )
        )
        self.assertEqual(builder_config.get_builder_type(), BuilderType.REMOTE)

        builder_config = BuilderConfig()
        self.assertEqual(builder_config.get_builder_type(), BuilderType.UNAVAILABLE)

        # if both local is preferred
        builder_config = BuilderConfig(
            local_builder_creds=BuilderApiKeyCreds(
                key="019894b9-cb40-79c4-b2bd-6aecb6f8c6c5",
                secret="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
                passphrase="1816e5ed89518467ffa78c65a2d6a62d240f6fd6d159cba7b2c4dc510800f75a",
            ),
            remote_builder_config=RemoteBuilderConfig(
                url="http://localhost:3000/sign",
            ),
        )
        self.assertEqual(builder_config.get_builder_type(), BuilderType.LOCAL)

    def test_generate_builder_headers(self):
        builder_config = BuilderConfig(
            local_builder_creds=BuilderApiKeyCreds(
                key="019894b9-cb40-79c4-b2bd-6aecb6f8c6c5",
                secret="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
                passphrase="1816e5ed89518467ffa78c65a2d6a62d240f6fd6d159cba7b2c4dc510800f75a",
            )
        )

        requestPath = "/order"
        requestBody = '{"deferExec":false,"order":{"salt":718139292476,"maker":"0x6e0c80c90ea6c15917308F820Eac91Ce2724B5b5","signer":"0x6e0c80c90ea6c15917308F820Eac91Ce2724B5b5","taker":"0x0000000000000000000000000000000000000000","tokenId":"15871154585880608648532107628464183779895785213830018178010423617714102767076","makerAmount":"5000000","takerAmount":"10000000","side":"BUY","expiration":"0","nonce":"0","feeRateBps":"1000","signatureType":0,"signature":"0x64a2b097cf14f9a24403748b4060bedf8f33f3dbe2a38e5f85bc2a5f2b841af633a2afcc9c4d57e60e4ff1d58df2756b2ca469f984ecfd46cb0c8baba8a0d6411b"},"owner":"5d1c266a-ed39-b9bd-c1f5-f24ae3e14a7b","orderType":"GTC"}'
        requestMethod = "POST"
        timestamp = 1758744060

        headers = builder_config.generate_builder_headers(
            requestMethod,
            requestPath,
            requestBody,
            timestamp,
        )

        self.assertIsNotNone(headers)
        self.assertEqual(
            headers.POLY_BUILDER_API_KEY, "019894b9-cb40-79c4-b2bd-6aecb6f8c6c5"
        )
        self.assertEqual(
            headers.POLY_BUILDER_PASSPHRASE,
            "1816e5ed89518467ffa78c65a2d6a62d240f6fd6d159cba7b2c4dc510800f75a",
        )
        self.assertEqual(headers.POLY_BUILDER_TIMESTAMP, "1758744060")
        self.assertEqual(
            headers.POLY_BUILDER_SIGNATURE,
            "8xh8d0qZHhBcLLYbsKNeiOW3Z0W2N5yNEq1kCVMe5QE=",
        )

    @responses.activate
    def test_generate_builder_headers_remote(self):
        mock_resp = BuilderHeaderPayload(
            POLY_BUILDER_API_KEY="019894b9-cb40-79c4-b2bd-6aecb6f8c6c5",
            POLY_BUILDER_TIMESTAMP="1758744060",
            POLY_BUILDER_PASSPHRASE="1816e5ed89518467ffa78c65a2d6a62d240f6fd6d159cba7b2c4dc510800f75a",
            POLY_BUILDER_SIGNATURE="8xh8d0qZHhBcLLYbsKNeiOW3Z0W2N5yNEq1kCVMe5QE=",
        ).to_dict()
        responses.add(
            responses.POST, "http://localhost:3000/sign", json=mock_resp, status=200
        )

        builder_config = BuilderConfig(
            remote_builder_config=RemoteBuilderConfig(
                url="http://localhost:3000/sign",
            )
        )
        requestPath = "/order"
        requestBody = '{"deferExec":false,"order":{"salt":718139292476,"maker":"0x6e0c80c90ea6c15917308F820Eac91Ce2724B5b5","signer":"0x6e0c80c90ea6c15917308F820Eac91Ce2724B5b5","taker":"0x0000000000000000000000000000000000000000","tokenId":"15871154585880608648532107628464183779895785213830018178010423617714102767076","makerAmount":"5000000","takerAmount":"10000000","side":"BUY","expiration":"0","nonce":"0","feeRateBps":"1000","signatureType":0,"signature":"0x64a2b097cf14f9a24403748b4060bedf8f33f3dbe2a38e5f85bc2a5f2b841af633a2afcc9c4d57e60e4ff1d58df2756b2ca469f984ecfd46cb0c8baba8a0d6411b"},"owner":"5d1c266a-ed39-b9bd-c1f5-f24ae3e14a7b","orderType":"GTC"}'
        requestMethod = "POST"
        timestamp = 1758744060

        headers = builder_config.generate_builder_headers(
            requestMethod,
            requestPath,
            requestBody,
            timestamp,
        )
        self.assertIsNotNone(headers)
        self.assertEqual(
            headers.get("POLY_BUILDER_API_KEY"), "019894b9-cb40-79c4-b2bd-6aecb6f8c6c5"
        )
        self.assertEqual(
            headers.get("POLY_BUILDER_PASSPHRASE"),
            "1816e5ed89518467ffa78c65a2d6a62d240f6fd6d159cba7b2c4dc510800f75a",
        )
        self.assertEqual(headers.get("POLY_BUILDER_TIMESTAMP"), "1758744060")
        self.assertEqual(
            headers.get("POLY_BUILDER_SIGNATURE"),
            "8xh8d0qZHhBcLLYbsKNeiOW3Z0W2N5yNEq1kCVMe5QE=",
        )
