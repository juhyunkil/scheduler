FROM ubuntu:16.04
ADD scheduler /scheduler
ENTRYPOINT ["/scheduler"]
