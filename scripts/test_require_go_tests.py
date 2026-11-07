import importlib.util
import io
import json
import pathlib
import unittest
from contextlib import redirect_stdout

spec = importlib.util.spec_from_file_location("require_go_tests", pathlib.Path(__file__).with_name("require-go-tests.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class RequiredGoTests(unittest.TestCase):
    def test_requires_pass_and_rejects_skips(self):
        with redirect_stdout(io.StringIO()):
            module.check(['{"Action":"pass","Test":"TestOne"}'])
            for lines in ([], ['{"Action":"skip","Test":"TestOne"}'],
                          ['{"Action":"pass","Test":"TestOne"}', '{"Action":"skip","Test":"TestTwo"}']):
                with self.subTest(lines=lines), self.assertRaises(ValueError):
                    module.check(lines)

    def test_requires_each_discovered_package_to_finish_with_tests(self):
        def events(package, with_test=True):
            result = [{"Action": "pass", "Package": package}]
            if with_test:
                result.insert(0, {"Action": "pass", "Package": package, "Test": "TestSameName"})
            return result

        first, second = events("one/e2e"), events("two/e2e")
        with redirect_stdout(io.StringIO()):
            module.check(map(json.dumps, first + second), packages=["one/e2e", "two/e2e"])
            for invalid in (first, first + events("two/e2e", False), first + second[:-1],
                            first + second + [{"Action": "fail", "Package": "two/e2e"}]):
                with self.subTest(events=invalid), self.assertRaises(ValueError):
                    module.check(map(json.dumps, invalid), packages=["one/e2e", "two/e2e"])


if __name__ == "__main__":
    unittest.main()
