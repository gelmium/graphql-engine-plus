FROM hasura/graphql-engine:v2.31.0
WORKDIR /graphql-engine
# install hasura cli
RUN curl -L https://github.com/hasura/graphql-engine/raw/stable/cli/get.sh | bash

# install nginx and envsubst
# temp fix for Ubuntu 22 issue of libssl1.1
# RUN echo "deb http://security.ubuntu.com/ubuntu focal-security main" | tee /etc/apt/sources.list.d/focal-security.list
RUN echo "deb http://security.ubuntu.com/ubuntu focal-security main" | tee /etc/apt/sources.list.d/focal-security.list \
    && apt update \
    && apt install -y gnupg2 libssl1.1 \
    && apt-get clean \
    && rm -rf /tmp/* /var/tmp/* /var/cache/apt/* /var/lib/apt/lists/* \
    && rm -f /etc/apt/sources.list.d/focal-security.list
RUN echo "deb https://nginx.org/packages/ubuntu/ focal nginx" >> /etc/apt/sources.list
RUN echo "deb-src https://nginx.org/packages/ubuntu/ focal nginx" >> /etc/apt/sources.list
RUN apt-key adv --keyserver keyserver.ubuntu.com --recv-keys ABF5BD827BD9BF62
RUN apt update \
    && apt install -y nginx gettext-base \
    && apt-get clean \
    && rm -rf /tmp/* /var/tmp/* /var/cache/apt/* /var/lib/apt/lists/*

# install python and pip
RUN apt update \
    && apt install -y python3 python3-pip \
    && apt-get clean \
    && rm -rf /tmp/* /var/tmp/* /var/cache/apt/* /var/lib/apt/lists/*

ADD ./scripting/requirements.txt /graphql-engine/scripting/requirements.txt
RUN pip3 install -r /graphql-engine/scripting/requirements.txt \
    && rm -rf /tmp/* /var/tmp/* /root/.cache/pip/*

# Add start script and nginx conf
COPY ./scripting /graphql-engine/scripting
COPY ./proxy/conf /var/www/conf
ADD ./proxy/container_start.sh /root/container_start.sh
ADD ./migrate_hasura.sh /root/migrate_hasura.sh

CMD /root/container_start.sh
EXPOSE 8000
# Add healthcheck
HEALTHCHECK --interval=30s --timeout=3s CMD curl --fail http://localhost:8888/health/engine || exit 1
