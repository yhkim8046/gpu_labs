# Security policy

GPU Lab은 로컬 kind 클러스터에서 실행되는 교육용 도구입니다. 기본 구현은 host GPU device, Docker socket, cloud credential을 애플리케이션 Pod에 노출하지 않습니다.

보안 취약점을 공개 issue에 먼저 올리지 마세요. GitHub repository의 private vulnerability reporting 또는 repository maintainer에게 비공개로 다음 정보를 보내 주세요.

- 영향받는 version/commit
- 재현 절차와 최소 예제
- 예상 영향과 이미 알고 있는 완화 방법
- 공개해도 되는 연락 방법

비밀번호, kubeconfig, registry token, private image URL, 개인 식별 정보는 첨부하지 마세요. 취약점 수정과 release가 끝난 뒤 maintainer가 공개 disclosure 시점을 조율합니다.

GPU Lab의 synthetic XID, health, exporter fault는 실제 GPU 보안 문제나 hardware incident가 아닙니다. 실제 환경의 driver/DCGM 보안 문제는 NVIDIA와 사용 중인 Kubernetes distribution의 보안 절차를 함께 따르세요.
