"""Check connect-only traces against exact fixture destinations, without payload capture."""
import argparse
import collections
import ipaddress
import json
from pathlib import Path
import re
import subprocess


def destinations(text):
    counts = collections.Counter()
    for line in text.splitlines():
        if 'connect(' not in line or 'AF_INET' not in line:
            continue
        port = re.search(r'sin6?_port=htons\((\d+)\)', line)
        address = re.search(r'inet_addr\("([^"]+)"\)', line)
        if address is None:
            address = re.search(r'inet_pton\(AF_INET6, "([^"]+)"', line)
        assert port and address, 'unrecognized Internet socket destination in trace'
        parsed = ipaddress.ip_address(address.group(1))
        if isinstance(parsed, ipaddress.IPv6Address) and parsed.ipv4_mapped:
            parsed = parsed.ipv4_mapped
        counts[(str(parsed), int(port.group(1)))] += 1
    return counts


def check(directory):
    compose = ['docker', 'compose', '-f', str(directory / 'compose.yml')]

    def address(service):
        container = subprocess.check_output(compose + ['ps', '-q', service], text=True).strip()
        assert container, 'fixture service is not running: ' + service
        raw = subprocess.check_output(['docker', 'inspect', '--format',
                                       '{{json .NetworkSettings.Networks}}', container], text=True)
        networks = json.loads(raw)
        assert len(networks) == 1, 'fixture service must have only one network'
        return next(iter(networks.values()))['IPAddress']

    database, mail, edge = address('database'), address('mail'), address('edge')
    dns = {('127.0.0.11', 53)}
    expected = {
        'api': {(database, 5432)},
        'worker': {(database, 5432), (mail, 1025)},
        'cli': {(edge, 443)},
    }
    summary = {}
    for service, required in expected.items():
        seen = destinations((directory / (service + '-connect.log')).read_text())
        assert required <= set(seen), 'missing exercised destination for ' + service
        assert set(seen) <= required | dns, 'unexpected socket destination for ' + service
        summary[service] = [{'address': host, 'port': port, 'attempts': count}
                            for (host, port), count in sorted(seen.items())]
    print(json.dumps(summary, indent=2))
    print('PASS: captured startup/auth/SMTP/CLI connect destinations match exact fixture allowlists')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path)
    check(parser.parse_args().directory)
