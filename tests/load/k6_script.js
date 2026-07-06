import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  stages: [
    { duration: '5s', target: 20 },  // Ramp-up to 20 virtual users (VUs)
    { duration: '10s', target: 20 }, // Hold at 20 VUs
    { duration: '5s', target: 0 },   // Ramp-down to 0 VUs
  ],
  thresholds: {
    http_req_failed: ['rate<0.05'], // Error rate must be less than 5%
    http_req_duration: ['p(95)<200'], // 95% of requests must complete under 200ms
  },
};

export default function () {
  const url = 'http://localhost:8080/catalog/items';
  const params = {
    headers: {
      'X-API-Key': 'alice-secret-key',
    },
  };

  const res = http.get(url, params);

  check(res, {
    'status is 200': (r) => r.status === 200,
    'body includes replica': (r) => r.body.includes('replica'),
  });

  sleep(0.1); // Sleep 100ms between requests per VU to manage load sensibly
}
